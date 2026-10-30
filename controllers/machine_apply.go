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
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

const (
	// tcpManagerName is the field manager of the Machine fields that the TalosControlPlane controller applies.
	tcpManagerName = "capi-taloscontrolplane"

	// classicManager is the field manager of the updates made without server-side apply: the name of the binary.
	classicManager = "manager"

	// machineApplyCacheTTL is how long an apply that did not change the Machine is remembered.
	machineApplyCacheTTL = 30 * time.Minute
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

	var requestID string

	if original != nil && r.machineApplyCache != nil {
		requestID, err = machineApplyRequestID(original.GetResourceVersion(), modified)
		if err != nil {
			return err
		}

		if _, ok := r.machineApplyCache.Get(requestID); ok {
			*machine = *original.DeepCopy()

			return nil
		}
	}

	beforeApply := modified.DeepCopy()

	if err := r.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(modified), client.ForceOwnership, client.FieldOwner(tcpManagerName)); err != nil {
		return errors.Wrapf(err, "failed to apply machine %s", machine.Name)
	}

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(modified.Object, machine); err != nil {
		return errors.Wrapf(err, "failed to convert applied machine %s", machine.Name)
	}

	if r.machineApplyCache == nil {
		return nil
	}

	// Remember the request against the resulting version: the Machine read on the next reconcile is that version.
	requestID, err = machineApplyRequestID(modified.GetResourceVersion(), beforeApply)
	if err != nil {
		return err
	}

	r.machineApplyCache.Set(requestID, struct{}{}, machineApplyCacheTTL)

	return nil
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

// machineApplyRequestID identifies an apply of the desired Machine to a version of the Machine.
func machineApplyRequestID(resourceVersion string, modified *unstructured.Unstructured) (string, error) {
	content, err := json.Marshal(modified.Object)
	if err != nil {
		return "", errors.Wrap(err, "failed to compute the machine apply request identifier")
	}

	hasher := fnv.New64a()
	_, _ = hasher.Write(content)

	return fmt.Sprintf("%s/%s/%s/%x", modified.GetNamespace(), modified.GetName(), resourceVersion, hasher.Sum64()), nil
}

// adoptMachineForServerSideApply hands the fields of a Machine created before the controller used server-side apply
// over to its apply field manager, the way Cluster API migrates the managed fields of its objects.
//
// It drops the managed fields of the classic manager, which created the Machine, and seeds the apply field manager,
// so that the next apply owns every field it sets: a field it stops setting is then removed. Fields the classic
// manager set that are not applied anymore are left as they are.
func (r *TalosControlPlaneReconciler) adoptMachineForServerSideApply(ctx context.Context, machine *clusterv1.Machine) error {
	for _, managedField := range machine.GetManagedFields() {
		if managedField.Manager == tcpManagerName &&
			managedField.Operation == metav1.ManagedFieldsOperationApply &&
			managedField.Subresource == "" {
			return nil
		}
	}

	base := machine.DeepCopy()

	managedFields := make([]metav1.ManagedFieldsEntry, 0, len(machine.GetManagedFields())+1)
	for _, managedField := range machine.GetManagedFields() {
		if managedField.Manager == classicManager &&
			managedField.Operation == metav1.ManagedFieldsOperationUpdate &&
			managedField.Subresource == "" {
			continue
		}

		managedFields = append(managedFields, managedField)
	}

	managedFields = append(managedFields, metav1.ManagedFieldsEntry{
		Manager:    tcpManagerName,
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: clusterv1.GroupVersion.String(),
		Time:       ptr.To(metav1.Now()),
		FieldsType: "FieldsV1",
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:name":{}}}`)},
	})

	machine.SetManagedFields(managedFields)

	if err := r.Client.Patch(ctx, machine, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return errors.Wrapf(err, "failed to migrate the managed fields of machine %s", machine.Name)
	}

	return nil
}
