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
func (r *TalosControlPlaneReconciler) applyMetadata(ctx context.Context, obj client.Object, labels, annotations map[string]string) error {
	gvk, err := apiutil.GVKForObject(obj, r.Client.Scheme())
	if err != nil {
		return errors.Wrapf(err, "failed to get the kind of %s", obj.GetName())
	}

	modified := &unstructured.Unstructured{}
	modified.SetGroupVersionKind(gvk)
	modified.SetNamespace(obj.GetNamespace())
	modified.SetName(obj.GetName())
	// The UID makes sure the apply only updates the object, and never creates it again after it was deleted.
	modified.SetUID(obj.GetUID())
	modified.SetLabels(labels)
	modified.SetAnnotations(annotations)

	if _, err := r.apply(ctx, modified, obj.GetResourceVersion(), tcpMetadataManagerName); err != nil {
		return errors.Wrapf(err, "failed to apply the labels and annotations of %s %s", gvk.Kind, obj.GetName())
	}

	return nil
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

// migrateManagedFields hands the fields of an object the controller created or updated without server-side apply
// over to the apply field manager, once, like Cluster API's ssa.MigrateManagedFields.
//
// It drops the managed fields of the classic manager and seeds the apply field manager, so that the next apply owns
// every field it sets: a field it stops setting is then removed. Fields the classic manager set that are not applied
// anymore are left as they are, unowned.
func (r *TalosControlPlaneReconciler) migrateManagedFields(ctx context.Context, obj client.Object, fieldManager string) error {
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
			continue
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
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:name":{}}}`)},
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
