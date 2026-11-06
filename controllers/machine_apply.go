// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

const (
	// tcpManagerName is the field manager of the Machine fields that the TalosControlPlane controller applies.
	tcpManagerName = "capi-taloscontrolplane"

	// tcpMetadataManagerName is the field manager of the labels and annotations that the TalosControlPlane
	// controller applies to the infrastructure machines and TalosConfigs of its Machines, like KubeadmControlPlane.
	tcpMetadataManagerName = "capi-taloscontrolplane-metadata"

	// applyCacheTTL is how long an apply that did not change the object is remembered.
	applyCacheTTL = 30 * time.Minute
)

// computeDesiredMachine computes the Machine of the TalosControlPlane, the way KubeadmControlPlane does.
//
// For a new Machine, the caller sets its name, infrastructure and bootstrap references and failure domain.
// For an existing Machine, they are kept, as well as its version, which only changes through a rollout,
// and the remediation it replaced.
func computeDesiredMachine(tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster, existing *clusterv1.Machine) *clusterv1.Machine {
	annotations := maps.Clone(tcp.Spec.MachineTemplate.ObjectMeta.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   tcp.Namespace,
			Labels:      controlPlaneMachineLabelsForCluster(tcp, cluster.Name),
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:     cluster.Name,
			Version:         tcp.Spec.Version,
			ReadinessGates:  slices.Clone(tcp.Spec.MachineTemplate.Spec.ReadinessGates),
			Deletion:        buildMachineDeletionSpec(tcp.Spec.MachineTemplate.Spec.Deletion),
			MinReadySeconds: copyInt32(tcp.Spec.MinReadySeconds),
		},
	}

	if existing == nil {
		// A Machine created while a remediation is in progress replaces the remediated Machine: record the remediation
		// on it right away, so that its retries are counted even if the remediation completes without it.
		if value, ok := tcp.Annotations[remediationInProgressAnnotation]; ok {
			machine.Annotations[remediationForAnnotation] = value
		}

		return machine
	}

	machine.Name = existing.Name
	machine.UID = existing.UID
	machine.Spec.Version = existing.Spec.Version
	machine.Spec.InfrastructureRef = existing.Spec.InfrastructureRef
	machine.Spec.Bootstrap.ConfigRef = existing.Spec.Bootstrap.ConfigRef
	machine.Spec.FailureDomain = existing.Spec.FailureDomain

	// Keep the remediation the Machine replaced, as it is applied together with the other annotations.
	if value, ok := existing.Annotations[remediationForAnnotation]; ok {
		machine.Annotations[remediationForAnnotation] = value
	}

	return machine
}

// applyMachine creates or updates the Machine with server-side apply, so that the labels, annotations and
// readiness gates removed from the machine template are removed from the Machine too.
//
// original is the Machine as last read, nil when it is created: an apply that did not change it is remembered,
// and not sent again until the Machine or the desired Machine changes.
// On success, machine holds the Machine returned by the API server.
func (r *TalosControlPlaneReconciler) applyMachine(ctx context.Context, machine *clusterv1.Machine, original *clusterv1.Machine) error {
	modified, err := prepareMachineForApply(r.Client.Scheme(), machine)
	if err != nil {
		return err
	}

	var originalResourceVersion string
	if original != nil {
		originalResourceVersion = original.GetResourceVersion()
	}

	applied, err := r.apply(ctx, modified, originalResourceVersion, tcpManagerName)
	if err != nil {
		return errors.Wrapf(err, "failed to apply machine %s", machine.Name)
	}

	if !applied {
		*machine = *original.DeepCopy()

		return nil
	}

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(modified.Object, machine); err != nil {
		return errors.Wrapf(err, "failed to convert applied machine %s", machine.Name)
	}

	return nil
}

// applyMetadata applies the labels and annotations of an object cloned for a Machine, the way KubeadmControlPlane
// does, so that the labels and annotations removed from the machine template are removed from it too.
//
// The apply is skipped when the object is already in sync, see metadataInSync. Unlike the Machines, the cloned
// objects are not skipped by the resource version: their providers keep writing to them, e.g. their status, which
// changes it on most reconciles.
func (r *TalosControlPlaneReconciler) applyMetadata(ctx context.Context, obj client.Object, labels, annotations map[string]string) error {
	gvk, err := apiutil.GVKForObject(obj, r.Client.Scheme())
	if err != nil {
		return errors.Wrapf(err, "failed to get the kind of %s", obj.GetName())
	}

	inSync, err := metadataInSync(obj, labels, annotations)
	if err != nil {
		return errors.Wrapf(err, "failed to compare the labels and annotations of %s %s", gvk.Kind, obj.GetName())
	}

	if inSync {
		return nil
	}

	modified := &unstructured.Unstructured{}
	modified.SetGroupVersionKind(gvk)
	modified.SetNamespace(obj.GetNamespace())
	modified.SetName(obj.GetName())
	// The UID makes sure the apply only updates the object, and never creates it again after it was deleted.
	modified.SetUID(obj.GetUID())
	modified.SetLabels(labels)
	modified.SetAnnotations(annotations)

	if err := r.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(modified), client.ForceOwnership, client.FieldOwner(tcpMetadataManagerName)); err != nil {
		return errors.Wrapf(err, "failed to apply the labels and annotations of %s %s", gvk.Kind, obj.GetName())
	}

	return nil
}

// metadataInSync reports whether applying the labels and annotations to the object would change nothing: it has
// them all, with their values, and the metadata field manager owns exactly their keys, so that no key removed from
// the machine template is left to remove.
func metadataInSync(obj client.Object, labels, annotations map[string]string) (bool, error) {
	if !hasAll(obj.GetLabels(), labels) || !hasAll(obj.GetAnnotations(), annotations) {
		return false, nil
	}

	for _, managedField := range obj.GetManagedFields() {
		if managedField.Manager != tcpMetadataManagerName ||
			managedField.Operation != metav1.ManagedFieldsOperationApply ||
			managedField.Subresource != "" {
			continue
		}

		fields := map[string]any{}
		if err := json.Unmarshal(managedField.FieldsV1.GetRawBytes(), &fields); err != nil {
			return false, errors.Wrap(err, "failed to unmarshal the managed fields")
		}

		metadata, _ := fields["f:metadata"].(map[string]any)

		return ownsExactly(metadata["f:labels"], labels) && ownsExactly(metadata["f:annotations"], annotations), nil
	}

	return false, nil
}

// hasAll reports whether current has every entry of desired.
func hasAll(current, desired map[string]string) bool {
	for key, value := range desired {
		if existing, ok := current[key]; !ok || existing != value {
			return false
		}
	}

	return true
}

// ownsExactly reports whether the managed fields of a map own exactly the keys of desired.
func ownsExactly(fields any, desired map[string]string) bool {
	owned, _ := fields.(map[string]any)

	count := 0

	for field := range owned {
		// "." is the map itself.
		if field == "." {
			continue
		}

		if _, ok := desired[strings.TrimPrefix(field, "f:")]; !ok {
			return false
		}

		count++
	}

	return count == len(desired)
}

// createClone creates an object cloned for a new Machine with server-side apply, like KubeadmControlPlane does.
//
// The object is created by the apply field manager without the given labels and annotations, which the metadata
// field manager then applies: it owns exactly them from the start, so that a key later removed from the machine
// template is removed from the object, and the object never goes through the managed fields migration. The labels
// and annotations are only missing until the second apply, before the Machine that the providers wait for exists.
//
// The object must not exist yet, as the apply would take it over. On success, obj holds the created object.
func (r *TalosControlPlaneReconciler) createClone(ctx context.Context, obj *unstructured.Unstructured, labels, annotations map[string]string) error {
	gvk := obj.GroupVersionKind()

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(gvk)

	// Read from the API server, as an object that was just created may not be in the cache yet.
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(obj), existing); err == nil {
		return errors.Errorf("%s %s already exists", gvk.Kind, obj.GetName())
	} else if !apierrors.IsNotFound(err) {
		return errors.Wrapf(err, "failed to check for an existing %s %s", gvk.Kind, obj.GetName())
	}

	created := obj.DeepCopy()
	created.SetLabels(withoutKeys(obj.GetLabels(), labels))
	created.SetAnnotations(withoutKeys(obj.GetAnnotations(), annotations))

	if err := r.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(created), client.ForceOwnership, client.FieldOwner(tcpManagerName)); err != nil {
		return errors.Wrapf(err, "failed to create %s %s", gvk.Kind, obj.GetName())
	}

	if err := r.applyMetadata(ctx, created, labels, annotations); err != nil {
		return err
	}

	*obj = *created

	return nil
}

// withoutKeys returns the entries of m whose keys are not in excluded.
func withoutKeys(m, excluded map[string]string) map[string]string {
	result := make(map[string]string, len(m))

	for key, value := range m {
		if _, ok := excluded[key]; !ok {
			result[key] = value
		}
	}

	return result
}

// apply applies the object with server-side apply as the field manager, and reports whether it was sent.
//
// originalResourceVersion is the version of the object as last read, empty when it is created: an apply that did not
// change it is remembered, and not sent again until the object or the applied object changes.
// On success, modified holds the object returned by the API server.
func (r *TalosControlPlaneReconciler) apply(ctx context.Context, modified *unstructured.Unstructured, originalResourceVersion, fieldManager string) (bool, error) {
	if originalResourceVersion != "" && r.applyCache != nil {
		requestID, err := applyRequestID(originalResourceVersion, modified)
		if err != nil {
			return false, err
		}

		if _, ok := r.applyCache.Get(requestID); ok {
			return false, nil
		}
	}

	beforeApply := modified.DeepCopy()

	if err := r.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(modified), client.ForceOwnership, client.FieldOwner(fieldManager)); err != nil {
		return false, err
	}

	if r.applyCache == nil {
		return true, nil
	}

	// Remember the request against the resulting version: the object read on the next reconcile is that version.
	requestID, err := applyRequestID(modified.GetResourceVersion(), beforeApply)
	if err != nil {
		return true, err
	}

	r.applyCache.Set(requestID, struct{}{}, applyCacheTTL)

	return true, nil
}

// prepareMachineForApply converts the Machine to unstructured, keeping only the fields the controller owns.
func prepareMachineForApply(scheme *runtime.Scheme, machine *clusterv1.Machine) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(machine)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to convert machine %s to unstructured", machine.Name)
	}

	modified := &unstructured.Unstructured{Object: map[string]any{
		"spec": content["spec"],
	}}

	gvks, _, err := scheme.ObjectKinds(machine)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get the kind of machine")
	}

	modified.SetGroupVersionKind(gvks[0])
	modified.SetName(machine.Name)
	modified.SetNamespace(machine.Namespace)
	modified.SetUID(machine.UID)
	modified.SetLabels(machine.Labels)
	modified.SetAnnotations(machine.Annotations)
	modified.SetOwnerReferences(machine.OwnerReferences)

	return modified, nil
}

// applyRequestID identifies an apply of the desired object to a version of the object.
func applyRequestID(resourceVersion string, modified *unstructured.Unstructured) (string, error) {
	content, err := json.Marshal(modified.Object)
	if err != nil {
		return "", errors.Wrap(err, "failed to compute the machine apply request identifier")
	}

	hasher := fnv.New64a()
	_, _ = hasher.Write(content)

	return fmt.Sprintf("%s/%s/%s/%x", modified.GetNamespace(), modified.GetName(), resourceVersion, hasher.Sum64()), nil
}

// classicManagerName returns the field manager the API server records for the updates the controller makes without
// server-side apply: the user agent of its client up to the first "/", which defaults to the name of the binary.
func classicManagerName(userAgent string) string {
	if userAgent == "" {
		userAgent = rest.DefaultKubernetesUserAgent()
	}

	name, _, _ := strings.Cut(userAgent, "/")

	return name
}

// adoptMachineForServerSideApply hands the fields of a Machine created before the controller used server-side apply
// over to its apply field manager, the way Cluster API migrates the managed fields of its objects.
//
// Fields the classic manager set that are not applied anymore are left as they are, see migrateManagedFields, so the
// spec fields the controller used to sync without server-side apply are first reset to the TalosControlPlane, like
// that sync did: only minReadySeconds, the other spec fields the controller applies were not set by earlier versions.
func (r *TalosControlPlaneReconciler) adoptMachineForServerSideApply(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error {
	if hasApplyManagedFields(machine, tcpManagerName) {
		return nil
	}

	// Reset before the managed fields are migrated, as the classic manager: the migration then drops the managed
	// fields of the reset too, so that the apply owns minReadySeconds when the TalosControlPlane sets it.
	if !ptr.Equal(machine.Spec.MinReadySeconds, tcp.Spec.MinReadySeconds) {
		base := machine.DeepCopy()
		machine.Spec.MinReadySeconds = copyInt32(tcp.Spec.MinReadySeconds)

		if err := r.Client.Patch(ctx, machine, client.MergeFrom(base), client.FieldOwner(r.classicManager)); err != nil {
			return errors.Wrapf(err, "failed to reset the minReadySeconds of machine %s", machine.Name)
		}
	}

	if err := r.migrateManagedFields(ctx, machine, tcpManagerName); err != nil {
		return errors.Wrapf(err, "failed to migrate the managed fields of machine %s", machine.Name)
	}

	return nil
}

// migrateManagedFields hands the fields of a Machine the controller created or updated without server-side apply
// over to the apply field manager, once, like Cluster API's ssa.MigrateManagedFields.
//
// It drops the managed fields of the classic manager and seeds the apply field manager, so that the next apply owns
// every field it sets: a field it stops setting is then removed. Fields the classic manager set that are not applied
// anymore are left as they are, unowned.
func (r *TalosControlPlaneReconciler) migrateManagedFields(ctx context.Context, obj client.Object, fieldManager string) error {
	return r.seedApplyManagedFields(ctx, obj, fieldManager, func(metav1.ManagedFieldsEntry) (*metav1.ManagedFieldsEntry, error) {
		return nil, nil
	})
}

// migrateClonedObjectManagedFields hands the labels and annotations of an object cloned for a Machine by an earlier
// release, which created it without server-side apply, over to the metadata field manager, once.
//
// Only the ownership of the given label and annotation keys is dropped from the managed fields of the classic
// manager: the bootstrap and infrastructure providers also write to the cloned objects, and may record their updates
// under the same manager name, the default one of a controller binary named "manager". Their other fields keep
// their owner. A key removed from the machine template before the upgrade is not among the given ones: it stays,
// owned by the classic manager.
func (r *TalosControlPlaneReconciler) migrateClonedObjectManagedFields(ctx context.Context, obj client.Object, labels, annotations map[string]string) error {
	return r.seedApplyManagedFields(ctx, obj, tcpMetadataManagerName, func(managedField metav1.ManagedFieldsEntry) (*metav1.ManagedFieldsEntry, error) {
		fields := map[string]any{}
		if err := json.Unmarshal(managedField.FieldsV1.GetRawBytes(), &fields); err != nil {
			return nil, errors.Wrapf(err, "failed to unmarshal the managed fields of %s", obj.GetName())
		}

		metadata, _ := fields["f:metadata"].(map[string]any)

		for section, keys := range map[string]map[string]string{"f:labels": labels, "f:annotations": annotations} {
			owned, _ := metadata[section].(map[string]any)

			for key := range keys {
				delete(owned, "f:"+key)
			}

			if owned != nil && len(owned) == 0 {
				delete(metadata, section)
			}
		}

		if metadata != nil && len(metadata) == 0 {
			delete(fields, "f:metadata")
		}

		if len(fields) == 0 {
			return nil, nil
		}

		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to marshal the managed fields of %s", obj.GetName())
		}

		managedField.FieldsV1 = metav1.NewFieldsV1(string(raw))

		return &managedField, nil
	})
}

// seedApplyManagedFields migrates the managed fields of an object to server-side apply, once, unless the field
// manager already applied it.
//
// Each entry the classic manager recorded for its updates goes through migrate, which returns the entry to keep in
// its place, or nil to drop it. The field manager is seeded, so that its first apply does not share the ownership
// of the fields it sets with an entry inferred by the API server.
func (r *TalosControlPlaneReconciler) seedApplyManagedFields(ctx context.Context, obj client.Object, fieldManager string,
	migrate func(metav1.ManagedFieldsEntry) (*metav1.ManagedFieldsEntry, error),
) error {
	if hasApplyManagedFields(obj, fieldManager) {
		return nil
	}

	base, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return errors.Errorf("failed to copy %s", obj.GetName())
	}

	managedFields := make([]metav1.ManagedFieldsEntry, 0, len(obj.GetManagedFields())+1)
	for _, managedField := range obj.GetManagedFields() {
		if managedField.Manager == r.classicManager &&
			managedField.Operation == metav1.ManagedFieldsOperationUpdate &&
			managedField.Subresource == "" {
			migrated, err := migrate(managedField)
			if err != nil {
				return err
			}

			if migrated == nil {
				continue
			}

			managedField = *migrated
		}

		managedFields = append(managedFields, managedField)
	}

	gvk, err := apiutil.GVKForObject(obj, r.Client.Scheme())
	if err != nil {
		return err
	}

	managedFields = append(managedFields, metav1.ManagedFieldsEntry{
		Manager:    fieldManager,
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: gvk.GroupVersion().String(),
		Time:       ptr.To(metav1.Now()),
		FieldsType: "FieldsV1",
		FieldsV1:   metav1.NewFieldsV1(`{"f:metadata":{"f:name":{}}}`),
	})

	obj.SetManagedFields(managedFields)

	return r.Client.Patch(ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// hasApplyManagedFields reports whether the field manager applied the object.
func hasApplyManagedFields(obj client.Object, fieldManager string) bool {
	for _, managedField := range obj.GetManagedFields() {
		if managedField.Manager == fieldManager &&
			managedField.Operation == metav1.ManagedFieldsOperationApply &&
			managedField.Subresource == "" {
			return true
		}
	}

	return false
}
