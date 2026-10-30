// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryconversion "k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	cpv1beta1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// apiVersionGetter resolves a contract-versioned apiGroup/kind to a fully qualified apiVersion.
// The controller is expected to inject a getter at startup using SetAPIVersionGetter; with the default
// implementation, the apiVersion of the down-converted references stays empty.
var apiVersionGetter = func(_ schema.GroupKind) (string, error) {
	return "", errors.New("apiVersionGetter not set")
}

// SetAPIVersionGetter wires the function used to resolve a contract-versioned reference back to a
// fully qualified apiVersion when down-converting to v1alpha3.
func SetAPIVersionGetter(f func(gk schema.GroupKind) (string, error)) {
	apiVersionGetter = f
}

// ConvertTo converts this TalosControlPlane to the Hub version (v1beta1).
func (src *TalosControlPlane) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*cpv1beta1.TalosControlPlane)
	if err := Convert_v1alpha3_TalosControlPlane_To_v1beta1_TalosControlPlane(src, dst, nil); err != nil {
		return err
	}

	if src.Spec.InfrastructureTemplate.Name != "" {
		if err := Convert_v1_ObjectReference_To_v1beta2_ContractVersionedObjectReference(&src.Spec.InfrastructureTemplate, &dst.Spec.MachineTemplate.Spec.InfrastructureRef, nil); err != nil {
			return err
		}
	}

	// Manually restore data.
	restored := &cpv1beta1.TalosControlPlane{}
	ok, err := utilconversion.UnmarshalData(src, restored)
	if err != nil {
		return err
	}

	// Recover intent for bool values converted to *bool, unless the v1alpha3 status has the initialization.
	if src.Status.Initialization.ControlPlaneInitialized == nil {
		initialization := cpv1beta1.TalosControlPlaneInitializationStatus{}
		var restoredControlPlaneInitialized *bool
		if ok {
			restoredControlPlaneInitialized = restored.Status.Initialization.ControlPlaneInitialized
		}
		clusterv1.Convert_bool_To_Pointer_bool(src.Status.Initialized, ok, restoredControlPlaneInitialized, &initialization.ControlPlaneInitialized)
		if !reflect.DeepEqual(initialization, cpv1beta1.TalosControlPlaneInitializationStatus{}) {
			dst.Status.Initialization = initialization
		}
	}

	// Recover hub-only fields preserved via the data annotation.
	if ok {
		dst.Spec.MachineNamingStrategy = restored.Spec.MachineNamingStrategy
		dst.Spec.MachineTemplate.ObjectMeta = restored.Spec.MachineTemplate.ObjectMeta
		dst.Spec.MachineTemplate.Spec.ReadinessGates = restored.Spec.MachineTemplate.Spec.ReadinessGates
		dst.Spec.MachineTemplate.Spec.Deletion = restored.Spec.MachineTemplate.Spec.Deletion

		// The apiVersion is left empty on down-conversion when it cannot be resolved: recover the apiGroup.
		infraRef := &dst.Spec.MachineTemplate.Spec.InfrastructureRef
		restoredInfraRef := restored.Spec.MachineTemplate.Spec.InfrastructureRef
		if src.Spec.InfrastructureTemplate.APIVersion == "" && infraRef.APIGroup == "" && infraRef.Kind == restoredInfraRef.Kind {
			infraRef.APIGroup = restoredInfraRef.APIGroup
		}

		// UpdatedReplicas has no v1alpha3 field.
		if restored.Status.Deprecated != nil && restored.Status.Deprecated.V1Beta1 != nil && restored.Status.Deprecated.V1Beta1.UpdatedReplicas != 0 {
			dst.V1Beta1DeprecatedStatus().UpdatedReplicas = restored.Status.Deprecated.V1Beta1.UpdatedReplicas
		}

		// Recover intent for int32 values converted to *int32: an unset counter stays unset.
		clusterv1.Convert_int32_To_Pointer_int32(src.Status.Replicas, ok, restored.Status.Replicas, &dst.Status.Replicas)
		clusterv1.Convert_int32_To_Pointer_int32(src.Status.ReadyReplicas, ok, restored.Status.ReadyReplicas, &dst.Status.ReadyReplicas)
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this version (v1alpha3).
func (dst *TalosControlPlane) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*cpv1beta1.TalosControlPlane)
	if err := Convert_v1beta1_TalosControlPlane_To_v1alpha3_TalosControlPlane(src, dst, nil); err != nil {
		return err
	}

	if src.Spec.MachineTemplate.Spec.InfrastructureRef.IsDefined() {
		if err := Convert_v1beta2_ContractVersionedObjectReference_To_v1_ObjectReference(&src.Spec.MachineTemplate.Spec.InfrastructureRef, &dst.Spec.InfrastructureTemplate, nil); err != nil {
			return err
		}
		dst.Spec.InfrastructureTemplate.Namespace = src.Namespace
	}

	// Preserve Hub data on down-conversion.
	return utilconversion.MarshalData(src, dst)
}

// ConvertTo converts this TalosControlPlaneList to the Hub version (v1beta1).
// Each item goes through ConvertTo, which remaps the infrastructure reference and restores the hub-only fields.
func (src *TalosControlPlaneList) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*cpv1beta1.TalosControlPlaneList)
	dst.ListMeta = src.ListMeta

	dst.Items = make([]cpv1beta1.TalosControlPlane, len(src.Items))
	for i := range src.Items {
		if err := src.Items[i].ConvertTo(&dst.Items[i]); err != nil {
			return err
		}
	}

	return nil
}

// ConvertFrom converts from the Hub version (v1beta1) to this version (v1alpha3).
// Each item goes through ConvertFrom, which remaps the infrastructure reference and preserves the hub-only fields.
func (dst *TalosControlPlaneList) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*cpv1beta1.TalosControlPlaneList)
	dst.ListMeta = src.ListMeta

	dst.Items = make([]TalosControlPlane, len(src.Items))
	for i := range src.Items {
		if err := dst.Items[i].ConvertFrom(&src.Items[i]); err != nil {
			return err
		}
	}

	return nil
}

// Convert_v1alpha3_TalosControlPlaneSpec_To_v1beta1_TalosControlPlaneSpec wraps the autogenerated
// converter. The InfrastructureTemplate ↔ MachineTemplate.Spec.InfrastructureRef remap is handled
// in ConvertTo because it requires access to the cluster-wide apiVersion getter.
func Convert_v1alpha3_TalosControlPlaneSpec_To_v1beta1_TalosControlPlaneSpec(in *TalosControlPlaneSpec, out *cpv1beta1.TalosControlPlaneSpec, s apimachineryconversion.Scope) error {
	return autoConvert_v1alpha3_TalosControlPlaneSpec_To_v1beta1_TalosControlPlaneSpec(in, out, s)
}

// Convert_v1beta1_TalosControlPlaneSpec_To_v1alpha3_TalosControlPlaneSpec wraps the autogenerated
// converter. MachineTemplate / MachineNamingStrategy hub data is restored via the round-trip
// annotation in ConvertFrom.
func Convert_v1beta1_TalosControlPlaneSpec_To_v1alpha3_TalosControlPlaneSpec(in *cpv1beta1.TalosControlPlaneSpec, out *TalosControlPlaneSpec, s apimachineryconversion.Scope) error {
	return autoConvert_v1beta1_TalosControlPlaneSpec_To_v1alpha3_TalosControlPlaneSpec(in, out, s)
}

func Convert_v1alpha3_TalosControlPlaneStatus_To_v1beta1_TalosControlPlaneStatus(in *TalosControlPlaneStatus, out *cpv1beta1.TalosControlPlaneStatus, s apimachineryconversion.Scope) error {
	if err := autoConvert_v1alpha3_TalosControlPlaneStatus_To_v1beta1_TalosControlPlaneStatus(in, out, s); err != nil {
		return err
	}

	// Reset conditions from autogenerated conversions
	// NOTE: v1beta1 contract conditions should not automatically be converted into v1beta2 contract conditions.
	out.Conditions = nil

	// Retrieve new conditions (v1beta2) from the v1beta2 field.
	if in.V1Beta2 != nil {
		out.Conditions = in.V1Beta2.Conditions
	}

	// v0.6.0-alpha.0 stored the v1beta2 conditions in status.conditions: they are no legacy conditions.
	legacyConditions := in.Conditions
	if isV060Alpha0Status(in) {
		legacyConditions = nil

		if in.Conditions != nil {
			// The Cluster API conversion of a v1beta1 condition deliberately drops it: copy the fields both have.
			// The observedGeneration was not kept by the v1alpha3 schema, the next status update sets it again.
			out.Conditions = make([]metav1.Condition, len(in.Conditions))
			for i, condition := range in.Conditions {
				out.Conditions[i] = metav1.Condition{
					Type:               string(condition.Type),
					Status:             metav1.ConditionStatus(condition.Status),
					LastTransitionTime: condition.LastTransitionTime,
					Reason:             condition.Reason,
					Message:            condition.Message,
				}
			}
		}
	}

	// Move legacy conditions, failureReason, failureMessage and replica counters to the deprecated field,
	// only when there is any: a status without them has no deprecated field.
	deprecated := cpv1beta1.TalosControlPlaneV1Beta1DeprecatedStatus{
		FailureReason:       in.FailureReason,
		FailureMessage:      in.FailureMessage,
		UnavailableReplicas: in.UnavailableReplicas,
		Initialized:         in.Initialized,
		Ready:               in.Ready,
	}
	if legacyConditions != nil {
		clusterv1beta1.Convert_v1beta1_Conditions_To_v1beta2_Deprecated_V1Beta1_Conditions(&legacyConditions, &deprecated.Conditions)
	}
	if !reflect.DeepEqual(deprecated, cpv1beta1.TalosControlPlaneV1Beta1DeprecatedStatus{}) {
		out.Deprecated = &cpv1beta1.TalosControlPlaneDeprecatedStatus{V1Beta1: &deprecated}
	}

	// Move initialization to ControlPlaneInitialized, unless it is set already; ConvertTo refines the bool intent
	// against the restored hub data.
	if in.Initialized && in.Initialization.ControlPlaneInitialized == nil {
		out.Initialization.ControlPlaneInitialized = ptr.To(true)
	}

	return nil
}

func Convert_v1beta1_TalosControlPlaneStatus_To_v1alpha3_TalosControlPlaneStatus(in *cpv1beta1.TalosControlPlaneStatus, out *TalosControlPlaneStatus, s apimachineryconversion.Scope) error {
	if err := autoConvert_v1beta1_TalosControlPlaneStatus_To_v1alpha3_TalosControlPlaneStatus(in, out, s); err != nil {
		return err
	}

	// Reset conditions from autogenerated conversions
	// NOTE: v1beta2 contract conditions should not automatically be converted into legacy conditions.
	out.Conditions = nil

	// Retrieve legacy conditions (v1beta1), failureReason, failureMessage and replica counters from the deprecated field.
	if in.Deprecated != nil && in.Deprecated.V1Beta1 != nil {
		if in.Deprecated.V1Beta1.Conditions != nil {
			clusterv1beta1.Convert_v1beta2_Deprecated_V1Beta1_Conditions_To_v1beta1_Conditions(&in.Deprecated.V1Beta1.Conditions, &out.Conditions)
		}
		out.FailureReason = in.Deprecated.V1Beta1.FailureReason
		out.FailureMessage = in.Deprecated.V1Beta1.FailureMessage
		out.UnavailableReplicas = in.Deprecated.V1Beta1.UnavailableReplicas
		out.Ready = in.Deprecated.V1Beta1.Ready
	}

	// Initialized always derives from Initialization.ControlPlaneInitialized; this keeps the
	// legacy v1alpha3 surface consistent with the v1beta2 source of truth even when the
	// v1beta1 deprecated holder is absent (e.g. fresh hub objects that never round-tripped).
	out.Initialized = ptr.Deref(in.Initialization.ControlPlaneInitialized, false)

	// Move new conditions (v1beta2) to the v1beta2 field. It is also set when the status has the fields
	// v0.6.0-alpha.0 reported, so that its status.conditions are not taken for the ones of that release.
	if in.Conditions == nil && !isV060Alpha0Status(out) {
		return nil
	}
	out.V1Beta2 = &TalosControlPlaneV1Beta2Status{}
	out.V1Beta2.Conditions = in.Conditions

	return nil
}

// isV060Alpha0Status reports whether a v1alpha3 status was written by v0.6.0-alpha.0, which stored the v1beta2
// conditions in status.conditions. That release always set at least one of the fields it added to v1alpha3 next
// to its conditions, and never the v1beta2 field; earlier releases never set those fields, and the conversion
// from the hub sets the v1beta2 field whenever it sets them.
func isV060Alpha0Status(in *TalosControlPlaneStatus) bool {
	return in.V1Beta2 == nil &&
		(in.AvailableReplicas != nil || in.UpToDateReplicas != nil || in.Initialization.ControlPlaneInitialized != nil)
}

// Implement local conversion func because conversion-gen is not aware of conversion func in other
// packages (see https://github.com/kubernetes/code-generator/issues/94).

func Convert_v1beta1_Condition_To_v1_Condition(in *clusterv1beta1.Condition, out *metav1.Condition, s apimachineryconversion.Scope) error {
	return clusterv1beta1.Convert_v1beta1_Condition_To_v1_Condition(in, out, s)
}

func Convert_v1_Condition_To_v1beta1_Condition(in *metav1.Condition, out *clusterv1beta1.Condition, s apimachineryconversion.Scope) error {
	return clusterv1beta1.Convert_v1_Condition_To_v1beta1_Condition(in, out, s)
}

// Convert_v1_ObjectReference_To_v1beta2_ContractVersionedObjectReference keeps the group of the apiVersion,
// the version is resolved through the contract when the reference is used.
func Convert_v1_ObjectReference_To_v1beta2_ContractVersionedObjectReference(in *corev1.ObjectReference, out *clusterv1.ContractVersionedObjectReference, _ apimachineryconversion.Scope) error {
	var apiGroup string
	if in.APIVersion != "" {
		gv, err := schema.ParseGroupVersion(in.APIVersion)
		if err != nil {
			return fmt.Errorf("failed to convert object: failed to parse apiVersion: %v", err)
		}
		apiGroup = gv.Group
	}

	*out = clusterv1.ContractVersionedObjectReference{
		APIGroup: apiGroup,
		Kind:     in.Kind,
		Name:     in.Name,
	}

	return nil
}

// Convert_v1beta2_ContractVersionedObjectReference_To_v1_ObjectReference resolves the apiVersion through the
// apiVersionGetter. The namespace is left to the caller, as the reference does not carry it.
//
// When the apiVersion cannot be resolved, for example while the CRD of the kind is not installed, it is left empty
// instead of failing the conversion, so that the object stays readable in v1alpha3.
func Convert_v1beta2_ContractVersionedObjectReference_To_v1_ObjectReference(in *clusterv1.ContractVersionedObjectReference, out *corev1.ObjectReference, _ apimachineryconversion.Scope) error {
	apiVersion, err := apiVersionGetter(schema.GroupKind{
		Group: in.APIGroup,
		Kind:  in.Kind,
	})
	if err != nil {
		apiVersion = ""
	}

	*out = corev1.ObjectReference{
		APIVersion: apiVersion,
		Kind:       in.Kind,
		Name:       in.Name,
	}

	return nil
}
