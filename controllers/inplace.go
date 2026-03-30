// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/pkg/errors"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/collections"
)

// canUpdateMachineInPlace checks whether a machine can be updated in-place
// by calling the CanUpdateMachine runtime extension hook.
// Returns true if an extension can handle all the changes in-place.
func (r *TalosControlPlaneReconciler) canUpdateMachineInPlace(
	ctx context.Context,
	machine *clusterv1.Machine,
	controlPlane *ControlPlane,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("Machine", klog.KObj(machine))

	// In-place updates require the InPlaceUpdates feature gate.
	if !feature.Gates.Enabled(feature.InPlaceUpdates) {
		return false, nil
	}

	// For Talos Linux, all config changes can be applied in-place via
	// talosctl apply-config. If RuntimeClient is not available (CAPI internal
	// client is not exported), we skip the CanUpdateMachine hook and assume
	// in-place is always possible. The UpdateMachine hook (called by CAPI
	// Machine controller) will handle the actual apply and report failure
	// if the config change cannot be applied.
	if r.RuntimeClient == nil {
		log.Info("No RuntimeClient available — assuming in-place update is possible (Talos can apply all config changes)")
		return true, nil
	}

	// Check if there are any CanUpdateMachine extension handlers registered.
	extensionHandlers, err := r.RuntimeClient.GetAllExtensions(ctx, runtimehooksv1.CanUpdateMachine, machine)
	if err != nil {
		return false, errors.Wrap(err, "failed to get CanUpdateMachine extensions")
	}
	if len(extensionHandlers) == 0 {
		return false, nil
	}
	if len(extensionHandlers) > 1 {
		return false, errors.Errorf("found multiple CanUpdateMachine hooks (%s): only one hook is supported", strings.Join(extensionHandlers, ","))
	}

	// Build the CanUpdateMachine request with current and desired state.
	req, err := r.buildCanUpdateMachineRequest(ctx, machine, controlPlane)
	if err != nil {
		return false, errors.Wrapf(err, "failed to build CanUpdateMachine request for Machine %s", machine.Name)
	}

	// Call the extension.
	resp := &runtimehooksv1.CanUpdateMachineResponse{}
	if err := r.RuntimeClient.CallExtension(ctx, runtimehooksv1.CanUpdateMachine, machine, extensionHandlers[0], req, resp); err != nil {
		return false, errors.Wrapf(err, "failed to call CanUpdateMachine extension for Machine %s", machine.Name)
	}

	// Apply patches from the response to the request to see if the changes are fully covered.
	if err := applyCanUpdatePatches(req, resp); err != nil {
		return false, errors.Wrapf(err, "failed to apply patches from CanUpdateMachine response for Machine %s", machine.Name)
	}

	// Check if current (patched) and desired now match.
	matches, reasons, err := matchesCanUpdateRequest(req)
	if err != nil {
		return false, errors.Wrapf(err, "failed to compare objects after CanUpdateMachine for Machine %s", machine.Name)
	}
	if !matches {
		log.Info(fmt.Sprintf("Machine %s cannot be updated in-place by extension", machine.Name), "reasons", strings.Join(reasons, ", "))
		return false, nil
	}

	log.Info(fmt.Sprintf("Machine %s can be updated in-place", machine.Name))
	return true, nil
}

// triggerInPlaceUpdate marks a machine for in-place update by setting the
// UpdateInProgressAnnotation. The CAPI Machine controller will then call
// the UpdateMachine hook to perform the actual update.
func (r *TalosControlPlaneReconciler) triggerInPlaceUpdate(
	ctx context.Context,
	machine *clusterv1.Machine,
) error {
	log := ctrl.LoggerFrom(ctx).WithValues("Machine", klog.KObj(machine))
	log.Info(fmt.Sprintf("Triggering in-place update for Machine %s", machine.Name))

	if _, ok := machine.Annotations[clusterv1.UpdateInProgressAnnotation]; ok {
		// Already marked for in-place update.
		return nil
	}

	orig := machine.DeepCopy()
	if machine.Annotations == nil {
		machine.Annotations = map[string]string{}
	}
	machine.Annotations[clusterv1.UpdateInProgressAnnotation] = ""
	// Mark UpdateMachine hook as pending — CAPI Machine controller checks this
	// annotation before calling the hook. Without it, Machine controller just
	// logs "waiting for owning controller to mark UpdateMachine hook as pending".
	machine.Annotations["runtime.cluster.x-k8s.io/pending-hooks"] = "UpdateMachine"

	if err := r.Client.Patch(ctx, machine, client.MergeFrom(orig)); err != nil {
		return errors.Wrapf(err, "failed to trigger in-place update for Machine %s by setting the %s annotation",
			klog.KObj(machine), clusterv1.UpdateInProgressAnnotation)
	}

	log.Info(fmt.Sprintf("Triggered in-place update for Machine %s", machine.Name))
	return nil
}

// filterMachinesForInPlaceUpdate separates machines needing rollout into those
// that can be updated in-place and those that need rolling replacement.
// Machines that can be updated in-place are triggered and removed from the
// rolling rollout set.
func (r *TalosControlPlaneReconciler) filterMachinesForInPlaceUpdate(
	ctx context.Context,
	controlPlane *ControlPlane,
	machinesNeedingRollout collections.Machines,
) (collections.Machines, error) {
	// If in-place updates feature gate is disabled, return all machines as needing rollout.
	if !feature.Gates.Enabled(feature.InPlaceUpdates) {
		return machinesNeedingRollout, nil
	}
	// Note: RuntimeClient == nil is OK for Talos — canUpdateMachineInPlace handles it.

	// Skip machines that already have an in-place update in progress.
	remaining := collections.New()

	for _, machine := range machinesNeedingRollout {
		if _, inProgress := machine.Annotations[clusterv1.UpdateInProgressAnnotation]; inProgress {
			// Already in-place updating, skip from rollout list.
			r.Log.Info("Machine already has in-place update in progress, skipping from rollout",
				"machine", machine.Name)
			continue
		}

		canUpdate, err := r.canUpdateMachineInPlace(ctx, machine, controlPlane)
		if err != nil {
			r.Log.Error(err, "failed to check if machine can be updated in-place, falling back to rolling update",
				"machine", machine.Name)
			remaining.Insert(machine)
			continue
		}

		if canUpdate {
			if err := r.triggerInPlaceUpdate(ctx, machine); err != nil {
				r.Log.Error(err, "failed to trigger in-place update, falling back to rolling update",
					"machine", machine.Name)
				remaining.Insert(machine)
				continue
			}
			// Successfully triggered in-place update; do not include in rolling rollout.
			continue
		}

		// Cannot update in-place, needs rolling replacement.
		remaining.Insert(machine)
	}

	return remaining, nil
}

// buildCanUpdateMachineRequest constructs a CanUpdateMachineRequest with the
// current and desired state of the machine and its related objects.
func (r *TalosControlPlaneReconciler) buildCanUpdateMachineRequest(
	ctx context.Context,
	machine *clusterv1.Machine,
	controlPlane *ControlPlane,
) (*runtimehooksv1.CanUpdateMachineRequest, error) {
	tcp := controlPlane.TCP

	// Build the desired Machine spec (what we want it to look like).
	desiredMachine := buildDesiredMachine(machine, tcp)

	// Get current and desired infra machine state.
	currentInfraRaw, desiredInfraRaw, err := r.buildInfraMachineObjects(ctx, machine, controlPlane)
	if err != nil {
		return nil, err
	}

	// Get current and desired bootstrap config state.
	currentBootstrapRaw, desiredBootstrapRaw, err := r.buildBootstrapConfigObjects(machine, controlPlane)
	if err != nil {
		return nil, err
	}

	req := &runtimehooksv1.CanUpdateMachineRequest{
		Current: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:               *cleanupMachineForRequest(machine),
			InfrastructureMachine: currentInfraRaw,
			BootstrapConfig:       currentBootstrapRaw,
		},
		Desired: runtimehooksv1.CanUpdateMachineRequestObjects{
			Machine:               *cleanupMachineForRequest(desiredMachine),
			InfrastructureMachine: desiredInfraRaw,
			BootstrapConfig:       desiredBootstrapRaw,
		},
	}

	return req, nil
}

// buildDesiredMachine constructs the desired Machine spec based on the TCP spec.
func buildDesiredMachine(currentMachine *clusterv1.Machine, tcp *controlplanev1.TalosControlPlane) *clusterv1.Machine {
	desired := currentMachine.DeepCopy()
	desired.Spec.Version = tcp.Spec.Version
	return desired
}

// cleanupMachineForRequest creates a minimal Machine for the CanUpdateMachine request,
// including only the fields relevant for comparison.
func cleanupMachineForRequest(machine *clusterv1.Machine) *clusterv1.Machine {
	return &clusterv1.Machine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Machine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        machine.Name,
			Namespace:   machine.Namespace,
			Labels:      machine.Labels,
			Annotations: machine.Annotations,
		},
		Spec: *machine.Spec.DeepCopy(),
	}
}

// buildInfraMachineObjects returns the current and desired infra machine state as RawExtensions.
func (r *TalosControlPlaneReconciler) buildInfraMachineObjects(
	ctx context.Context,
	machine *clusterv1.Machine,
	controlPlane *ControlPlane,
) (runtime.RawExtension, runtime.RawExtension, error) {
	// Current infra machine.
	currentInfra, found := controlPlane.infraObjects[machine.Name]
	if !found {
		return runtime.RawExtension{}, runtime.RawExtension{}, errors.Errorf("infra machine not found for machine %s", machine.Name)
	}

	currentInfraRaw, err := convertUnstructuredToRawExtension(currentInfra)
	if err != nil {
		return runtime.RawExtension{}, runtime.RawExtension{}, errors.Wrap(err, "failed to convert current infra machine")
	}

	// Desired infra machine: for now use the current one as the base.
	// The infra template changes are detected via TemplateClonedFrom annotations;
	// if the template changed, the infra object will differ.
	// We pass the current infra as the desired as well because the actual desired
	// state of the infra machine is determined by the infrastructure template.
	// The extension is responsible for determining if it can handle the change.
	desiredInfraRaw := currentInfraRaw

	return currentInfraRaw, desiredInfraRaw, nil
}

// buildBootstrapConfigObjects returns the current and desired TalosConfig state as RawExtensions.
func (r *TalosControlPlaneReconciler) buildBootstrapConfigObjects(
	machine *clusterv1.Machine,
	controlPlane *ControlPlane,
) (runtime.RawExtension, runtime.RawExtension, error) {
	tcp := controlPlane.TCP

	// Current TalosConfig.
	currentConfig, found := controlPlane.talosConfigs[machine.Name]
	if !found {
		return runtime.RawExtension{}, runtime.RawExtension{}, errors.Errorf("talosconfig not found for machine %s", machine.Name)
	}

	currentRaw, err := convertTalosConfigToRawExtension(currentConfig)
	if err != nil {
		return runtime.RawExtension{}, runtime.RawExtension{}, errors.Wrap(err, "failed to convert current talos config")
	}

	// Desired TalosConfig: built from the TCP spec.
	desiredConfig := &cabptv1.TalosConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cabptv1.GroupVersion.String(),
			Kind:       "TalosConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      currentConfig.Name,
			Namespace: currentConfig.Namespace,
		},
		Spec: tcp.Spec.ControlPlaneConfig.ControlPlaneConfig,
	}

	desiredRaw, err := convertTalosConfigToRawExtension(desiredConfig)
	if err != nil {
		return runtime.RawExtension{}, runtime.RawExtension{}, errors.Wrap(err, "failed to convert desired talos config")
	}

	return currentRaw, desiredRaw, nil
}

// convertUnstructuredToRawExtension converts an Unstructured object to a runtime.RawExtension.
func convertUnstructuredToRawExtension(obj *unstructured.Unstructured) (runtime.RawExtension, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return runtime.RawExtension{}, errors.Wrap(err, "failed to marshal unstructured object")
	}
	return runtime.RawExtension{
		Raw:    raw,
		Object: obj.DeepCopy(),
	}, nil
}

// convertTalosConfigToRawExtension converts a TalosConfig to a runtime.RawExtension.
func convertTalosConfigToRawExtension(config *cabptv1.TalosConfig) (runtime.RawExtension, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return runtime.RawExtension{}, errors.Wrap(err, "failed to marshal talos config")
	}

	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(raw, u); err != nil {
		return runtime.RawExtension{}, errors.Wrap(err, "failed to convert talos config to unstructured")
	}

	return runtime.RawExtension{
		Raw:    raw,
		Object: u,
	}, nil
}

// applyCanUpdatePatches applies patches from the CanUpdateMachine response to the request.
func applyCanUpdatePatches(req *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse) error {
	if resp.MachinePatch.IsDefined() {
		if err := applyPatchToMachine(&req.Current.Machine, resp.MachinePatch); err != nil {
			return errors.Wrap(err, "failed to apply machine patch")
		}
	}

	if resp.BootstrapConfigPatch.IsDefined() {
		if err := applyPatchToRawExtension(&req.Current.BootstrapConfig, resp.BootstrapConfigPatch); err != nil {
			return errors.Wrap(err, "failed to apply bootstrap config patch")
		}
	}

	if resp.InfrastructureMachinePatch.IsDefined() {
		if err := applyPatchToRawExtension(&req.Current.InfrastructureMachine, resp.InfrastructureMachinePatch); err != nil {
			return errors.Wrap(err, "failed to apply infra machine patch")
		}
	}

	return nil
}

// applyPatchToMachine applies a patch to a Machine object.
func applyPatchToMachine(machine *clusterv1.Machine, patch runtimehooksv1.Patch) error {
	machineBytes, err := json.Marshal(machine)
	if err != nil {
		return err
	}

	patchedBytes, err := applyPatchBytes(machineBytes, patch)
	if err != nil {
		return err
	}

	return json.Unmarshal(patchedBytes, machine)
}

// applyPatchToRawExtension applies a patch to a runtime.RawExtension.
func applyPatchToRawExtension(obj *runtime.RawExtension, patch runtimehooksv1.Patch) error {
	if obj.Raw == nil {
		return errors.New("raw extension has no raw data")
	}

	patchedBytes, err := applyPatchBytes(obj.Raw, patch)
	if err != nil {
		return err
	}

	obj.Raw = patchedBytes

	// Also update the Object if it's set.
	if obj.Object != nil {
		u := &unstructured.Unstructured{}
		if err := json.Unmarshal(patchedBytes, u); err != nil {
			return err
		}
		obj.Object = u
	}

	return nil
}

// applyPatchBytes applies a JSON patch or JSON merge patch to raw bytes.
func applyPatchBytes(data []byte, patch runtimehooksv1.Patch) ([]byte, error) {
	switch patch.PatchType {
	case runtimehooksv1.JSONPatchType:
		jp, err := jsonpatch.DecodePatch(patch.Patch)
		if err != nil {
			return nil, errors.Wrap(err, "failed to decode JSON patch")
		}
		return jp.Apply(data)
	case runtimehooksv1.JSONMergePatchType:
		return jsonpatch.MergePatch(data, patch.Patch)
	default:
		return nil, errors.Errorf("unknown patch type: %s", patch.PatchType)
	}
}

// matchesCanUpdateRequest checks if the current (patched) and desired objects in the request match.
func matchesCanUpdateRequest(req *runtimehooksv1.CanUpdateMachineRequest) (bool, []string, error) {
	var reasons []string

	// Compare Machine specs.
	currentSpec, err := json.Marshal(req.Current.Machine.Spec)
	if err != nil {
		return false, nil, errors.Wrap(err, "failed to marshal current machine spec")
	}
	desiredSpec, err := json.Marshal(req.Desired.Machine.Spec)
	if err != nil {
		return false, nil, errors.Wrap(err, "failed to marshal desired machine spec")
	}
	if string(currentSpec) != string(desiredSpec) {
		reasons = append(reasons, "Machine spec does not match after patching")
	}

	// Compare bootstrap config specs.
	currentBootstrapSpec := extractSpec(req.Current.BootstrapConfig)
	desiredBootstrapSpec := extractSpec(req.Desired.BootstrapConfig)
	if currentBootstrapSpec != desiredBootstrapSpec {
		reasons = append(reasons, "BootstrapConfig spec does not match after patching")
	}

	// Compare infra machine specs.
	currentInfraSpec := extractSpec(req.Current.InfrastructureMachine)
	desiredInfraSpec := extractSpec(req.Desired.InfrastructureMachine)
	if currentInfraSpec != desiredInfraSpec {
		reasons = append(reasons, "InfrastructureMachine spec does not match after patching")
	}

	if len(reasons) > 0 {
		return false, reasons, nil
	}

	return true, nil, nil
}

// extractSpec extracts the "spec" field from a RawExtension as a JSON string for comparison.
func extractSpec(raw runtime.RawExtension) string {
	if raw.Raw == nil {
		return ""
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(raw.Raw, &obj); err != nil {
		return string(raw.Raw)
	}

	spec, ok := obj["spec"]
	if !ok {
		return ""
	}

	specBytes, err := json.Marshal(spec)
	if err != nil {
		return ""
	}

	return string(specBytes)
}
