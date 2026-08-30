// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	jsonpatch "github.com/evanphx/json-patch"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/collections"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/internal/hooks"
)

// inPlacePlan is everything needed to move one Machine to the desired state in place.
type inPlacePlan struct {
	machine             *clusterv1.Machine
	desiredMachine      *clusterv1.Machine
	desiredInfraMachine *unstructured.Unstructured
	desiredTalosConfig  *cabptv1.TalosConfig
}

// reconcileInPlaceUpdates offers each outdated control plane Machine to the in-place update
// extension, and triggers an in-place update for those it can fully handle.
//
// It returns the Machines it has claimed, which the caller excludes from the rollout set. A
// claimed Machine is either already updating or has just been triggered; either way it must
// not also be rolled out.
//
// TalosControlPlane has to implement this itself because it is not KubeadmControlPlane: the
// owner half of the in-place update contract lives in each control plane provider, while
// only the executor half (calling the UpdateMachine hook) is provided by core Cluster API.
func (r *TalosControlPlaneReconciler) reconcileInPlaceUpdates(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	controlPlane *ControlPlane,
	outdated collections.Machines,
) (collections.Machines, error) {
	claimed := collections.New()

	if !feature.Gates.Enabled(feature.InPlaceUpdates) || r.RuntimeClient == nil {
		return claimed, nil
	}

	// Machines already mid-update stay claimed so the rollout path leaves them alone until
	// the core Machine controller clears the annotation.
	for _, machine := range outdated {
		if isUpdatingInPlace(machine) {
			claimed.Insert(machine)
		}
	}

	// One control plane machine at a time. An in-place update can reboot the node (a Talos
	// upgrade always does), so concurrent updates would risk etcd quorum.
	if len(claimed) > 0 {
		return claimed, nil
	}

	candidate := r.nextInPlaceCandidate(controlPlane, outdated)
	if candidate == nil {
		return claimed, nil
	}

	log := r.Log.WithValues("machine", klog.KObj(candidate))

	// Only start an update while etcd is healthy across the whole control plane, for the
	// same reason: the node is about to become temporarily unavailable.
	if err := r.etcdHealthcheck(ctx, tcp, machineList(controlPlane.Machines)); err != nil {
		log.Info("skipping in-place update, etcd is not healthy", "error", err.Error())

		return claimed, nil
	}

	plan, err := r.buildInPlacePlan(tcp, controlPlane, candidate)
	if err != nil {
		return claimed, err
	}

	canUpdate, err := r.canUpdateMachine(ctx, plan, controlPlane)
	if err != nil {
		return claimed, err
	}

	if !canUpdate {
		log.Info("in-place update not possible for this change, falling back to rollout")

		return claimed, nil
	}

	if err := r.triggerInPlaceUpdate(ctx, plan); err != nil {
		return claimed, err
	}

	claimed.Insert(candidate)

	return claimed, nil
}

// nextInPlaceCandidate picks an outdated Machine that is eligible to be considered for an
// in-place update.
//
// Machines whose InfraMachine was cloned from a superseded template are deliberately
// excluded. Rotating an infrastructure template means the Machine needs a new InfraMachine,
// which this provider only produces by creating a replacement; claiming such a Machine would
// leave it outdated forever and spin.
func (r *TalosControlPlaneReconciler) nextInPlaceCandidate(controlPlane *ControlPlane, outdated collections.Machines) *clusterv1.Machine {
	matchesTemplate := MatchesTemplateClonedFrom(controlPlane.infraObjects, controlPlane.TCP)

	for _, machine := range outdated.SortedByCreationTimestamp() {
		if machine.DeletionTimestamp != nil {
			continue
		}

		if !matchesTemplate(machine) {
			continue
		}

		if !machine.Status.NodeRef.IsDefined() {
			continue
		}

		return machine
	}

	return nil
}

// buildInPlacePlan computes the desired Machine, InfraMachine and TalosConfig for a Machine.
//
// The desired state comes from the same fields MachinesWithOutdatedRolloutSpec compares
// against, so that a Machine judged outdated is judged up to date once the plan is applied.
// The InfraMachine is carried through unchanged: candidates whose infrastructure template
// has rotated were already filtered out, so infrastructure never differs here.
func (r *TalosControlPlaneReconciler) buildInPlacePlan(
	tcp *controlplanev1.TalosControlPlane,
	controlPlane *ControlPlane,
	machine *clusterv1.Machine,
) (*inPlacePlan, error) {
	desiredMachine := machine.DeepCopy()
	desiredMachine.Spec.Version = tcp.Spec.Version

	plan := &inPlacePlan{
		machine:             machine,
		desiredMachine:      desiredMachine,
		desiredInfraMachine: controlPlane.infraObjects[machine.Name],
	}

	current, ok := controlPlane.talosConfigs[machine.Name]
	if !ok {
		return nil, fmt.Errorf("cannot plan in-place update for %s: TalosConfig not found", klog.KObj(machine))
	}

	desiredTalosConfig := current.DeepCopy()
	desiredTalosConfig.Spec = tcp.Spec.ControlPlaneConfig.ControlPlaneConfig
	plan.desiredTalosConfig = desiredTalosConfig

	return plan, nil
}

// canUpdateMachine asks the registered extension whether it can absorb the whole change.
//
// The extension answers with per-object patches describing what it can handle. Applying
// those patches to the current objects and comparing against desired is what decides the
// outcome: anything left over means the extension declined some part of the change and the
// Machine has to be rolled out instead.
func (r *TalosControlPlaneReconciler) canUpdateMachine(ctx context.Context, plan *inPlacePlan, controlPlane *ControlPlane) (bool, error) {
	extensions, err := r.RuntimeClient.GetAllExtensions(ctx, runtimehooksv1.CanUpdateMachine, plan.machine)
	if err != nil {
		return false, err
	}

	switch len(extensions) {
	case 0:
		// Nothing registered: in-place updates are simply not available.
		return false, nil
	case 1:
	default:
		return false, fmt.Errorf("found %d CanUpdateMachine extensions, only one is supported", len(extensions))
	}

	currentTalosConfig := controlPlane.talosConfigs[plan.machine.Name]

	req := &runtimehooksv1.CanUpdateMachineRequest{
		Current: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:               *cleanMachine(plan.machine),
			InfrastructureMachine: rawFrom(plan.desiredInfraMachine),
			BootstrapConfig:       rawFrom(currentTalosConfig),
		},
		Desired: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:               *cleanMachine(plan.desiredMachine),
			InfrastructureMachine: rawFrom(plan.desiredInfraMachine),
			BootstrapConfig:       rawFrom(plan.desiredTalosConfig),
		},
	}

	resp := &runtimehooksv1.CanUpdateMachineResponse{}
	if err := r.RuntimeClient.CallExtension(ctx, runtimehooksv1.CanUpdateMachine, plan.machine, extensions[0], req, resp); err != nil {
		return false, err
	}

	if resp.Status == runtimehooksv1.ResponseStatusFailure {
		return false, fmt.Errorf("CanUpdateMachine hook failed: %s", resp.Message)
	}

	machineCovered, err := patchReaches(req.Current.Machine, req.Desired.Machine, resp.MachinePatch)
	if err != nil {
		return false, err
	}

	bootstrapCovered, err := patchReachesRaw(req.Current.BootstrapConfig, req.Desired.BootstrapConfig, resp.BootstrapConfigPatch)
	if err != nil {
		return false, err
	}

	infraCovered, err := patchReachesRaw(req.Current.InfrastructureMachine, req.Desired.InfrastructureMachine, resp.InfrastructureMachinePatch)
	if err != nil {
		return false, err
	}

	return machineCovered && bootstrapCovered && infraCovered, nil
}

// patchReaches reports whether applying patch to current produces desired.
func patchReaches(current, desired any, patch runtimehooksv1.Patch) (bool, error) {
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return false, err
	}

	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return false, err
	}

	return jsonReaches(currentJSON, desiredJSON, patch)
}

func patchReachesRaw(current, desired runtime.RawExtension, patch runtimehooksv1.Patch) (bool, error) {
	if len(current.Raw) == 0 && len(desired.Raw) == 0 {
		return true, nil
	}

	return jsonReaches(current.Raw, desired.Raw, patch)
}

func jsonReaches(currentJSON, desiredJSON []byte, patch runtimehooksv1.Patch) (bool, error) {
	patched := currentJSON

	if patch.IsDefined() {
		var err error

		switch patch.PatchType {
		case runtimehooksv1.JSONMergePatchType:
			patched, err = jsonpatch.MergePatch(currentJSON, patch.Patch)
		case runtimehooksv1.JSONPatchType:
			decoded, decodeErr := jsonpatch.DecodePatch(patch.Patch)
			if decodeErr != nil {
				return false, decodeErr
			}

			patched, err = decoded.Apply(currentJSON)
		default:
			return false, fmt.Errorf("unsupported patch type %q", patch.PatchType)
		}

		if err != nil {
			return false, err
		}
	}

	var got, want any

	if err := json.Unmarshal(patched, &got); err != nil {
		return false, err
	}

	if err := json.Unmarshal(desiredJSON, &want); err != nil {
		return false, err
	}

	return reflect.DeepEqual(got, want), nil
}

// cleanMachine reduces a Machine to the identity and spec the hook contract carries, so that
// status churn cannot make two otherwise identical Machines look different.
func cleanMachine(machine *clusterv1.Machine) *clusterv1.Machine {
	out := &clusterv1.Machine{}
	out.APIVersion = clusterv1.GroupVersion.String()
	out.Kind = "Machine"
	out.Name = machine.Name
	out.Namespace = machine.Namespace
	out.Labels = machine.Labels
	out.Annotations = machine.Annotations
	out.Spec = *machine.Spec.DeepCopy()

	return out
}

// rawFrom renders an object into the wire form the hook contract uses, keeping only
// apiVersion, kind, identity and spec.
func rawFrom(obj any) runtime.RawExtension {
	if obj == nil || reflect.ValueOf(obj).IsNil() {
		return runtime.RawExtension{}
	}

	encoded, err := json.Marshal(obj)
	if err != nil {
		return runtime.RawExtension{}
	}

	var full map[string]any
	if err := json.Unmarshal(encoded, &full); err != nil {
		return runtime.RawExtension{}
	}

	trimmed := map[string]any{
		"apiVersion": full["apiVersion"],
		"kind":       full["kind"],
		"spec":       full["spec"],
	}

	if metadata, ok := full["metadata"].(map[string]any); ok {
		trimmed["metadata"] = map[string]any{
			"name":      metadata["name"],
			"namespace": metadata["namespace"],
		}
	}

	out, err := json.Marshal(trimmed)
	if err != nil {
		return runtime.RawExtension{}
	}

	return runtime.RawExtension{Raw: out}
}

// machineList converts a Machine collection into the slice form the health checks take.
func machineList(machines collections.Machines) []clusterv1.Machine {
	out := make([]clusterv1.Machine, 0, len(machines))

	for _, machine := range machines {
		out = append(out, *machine)
	}

	return out
}

// isUpdatingInPlace reports whether a Machine is mid in-place update.
func isUpdatingInPlace(machine *clusterv1.Machine) bool {
	if _, ok := machine.Annotations[clusterv1.UpdateInProgressAnnotation]; ok {
		return true
	}

	return hooks.IsPending(runtimehooksv1.UpdateMachine, machine)
}
