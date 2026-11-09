// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validate/content"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager implements webhook methods.
func (r *TalosControlPlane) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1beta1-taloscontrolplane,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1beta1,name=default.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1
// +kubebuilder:webhook:verbs=create;update;delete,path=/validate-controlplane-cluster-x-k8s-io-v1beta1-taloscontrolplane,mutating=false,failurePolicy=fail,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1beta1,name=validate.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var (
	_ admission.Defaulter[*TalosControlPlane] = &TalosControlPlane{}
	_ admission.Validator[*TalosControlPlane] = &TalosControlPlane{}
)

// Default implements admission.Defaulter so a webhook will be registered for the type.
func (*TalosControlPlane) Default(_ context.Context, obj *TalosControlPlane) error {
	defaultTalosControlPlaneSpec(&obj.Spec)

	return nil
}

func defaultTalosControlPlaneSpec(s *TalosControlPlaneSpec) {
	if s.Replicas == nil {
		replicas := int32(1)
		s.Replicas = &replicas
	}

	if !strings.HasPrefix(s.Version, "v") {
		s.Version = "v" + s.Version
	}

	s.RolloutStrategy = defaultRolloutStrategy(s.RolloutStrategy)
}

func defaultRolloutStrategy(rolloutStrategy *RolloutStrategy) *RolloutStrategy {
	ios1 := intstr.FromInt(1)

	if rolloutStrategy == nil {
		rolloutStrategy = &RolloutStrategy{}
	}

	// Enforce RollingUpdate strategy and default MaxSurge if not set.
	if len(rolloutStrategy.Type) == 0 {
		rolloutStrategy.Type = RollingUpdateStrategyType
	}
	if rolloutStrategy.Type == RollingUpdateStrategyType {
		if rolloutStrategy.RollingUpdate == nil {
			rolloutStrategy.RollingUpdate = &RollingUpdate{}
		}
		rolloutStrategy.RollingUpdate.MaxSurge = intstr.ValueOrDefault(rolloutStrategy.RollingUpdate.MaxSurge, ios1)
	}

	return rolloutStrategy
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateCreate(_ context.Context, obj *TalosControlPlane) (admission.Warnings, error) {
	allErrs := obj.validate()
	allErrs = append(allErrs, validateMachineTaintsFeatureGate(obj.Spec.MachineTemplate.Spec.Taints, machineTemplateTaintsPath)...)

	return nil, obj.toErr(allErrs)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type.
//
// Like KubeadmControlPlane, the infrastructure reference may point to a template of another kind:
// the Machines cloned from the previous template are rolled out.
//
// The MachineTaintPropagation feature gate is only checked when the taints change: a TalosControlPlane whose taints
// were set while the gate was enabled can still be updated once it is disabled, so that the controller can keep
// patching its status and remove its finalizer.
func (*TalosControlPlane) ValidateUpdate(_ context.Context, oldObj, newObj *TalosControlPlane) (admission.Warnings, error) {
	allErrs := newObj.validate()

	if !apiequality.Semantic.DeepEqual(oldObj.Spec.MachineTemplate.Spec.Taints, newObj.Spec.MachineTemplate.Spec.Taints) {
		allErrs = append(allErrs, validateMachineTaintsFeatureGate(newObj.Spec.MachineTemplate.Spec.Taints, machineTemplateTaintsPath)...)
	}

	return nil, newObj.toErr(allErrs)
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateDelete(_ context.Context, _ *TalosControlPlane) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosControlPlane) toErr(allErrs field.ErrorList) error {
	if len(allErrs) == 0 {
		return nil
	}
	return newInvalidTalosControlPlaneError("TalosControlPlane", r.Name, allErrs)
}

func (r *TalosControlPlane) validate() field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateInfrastructureRef(r.Spec.MachineTemplate.Spec.InfrastructureRef, field.NewPath("spec", "machineTemplate", "spec", "infrastructureRef"))...)
	allErrs = append(allErrs, validateMachineNamingStrategy(r.Spec.MachineNamingStrategy, field.NewPath("spec", "machineNamingStrategy"))...)
	allErrs = append(allErrs, validateRolloutStrategy(r.Spec.RolloutStrategy, field.NewPath("spec", "rolloutStrategy"))...)
	allErrs = append(allErrs, validateMachineTaints(r.Spec.MachineTemplate.Spec.Taints, machineTemplateTaintsPath)...)

	return allErrs
}

func validateInfrastructureRef(ref clusterv1.ContractVersionedObjectReference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if ref.Name == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("name"), "name is required"))
	}
	if ref.Kind == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("kind"), "kind is required"))
	}
	if ref.APIGroup == "" {
		allErrs = append(allErrs, field.Required(fldPath.Child("apiGroup"), "apiGroup is required"))
	}

	return allErrs
}

// machineTemplateTaintsPath is the path of the taints of the machine template of a TalosControlPlane.
var machineTemplateTaintsPath = field.NewPath("spec", "machineTemplate", "spec", "taints")

// validateMachineTaintsFeatureGate forbids the taints of the machine template unless the MachineTaintPropagation
// feature gate is enabled, like KubeadmControlPlane does.
func validateMachineTaintsFeatureGate(taints []clusterv1.MachineTaint, fldPath *field.Path) field.ErrorList {
	if feature.Gates.Enabled(feature.MachineTaintPropagation) || len(taints) == 0 {
		return nil
	}

	return field.ErrorList{field.Forbidden(fldPath, "taints are not allowed to be set when the feature gate MachineTaintPropagation is disabled")}
}

// validateMachineTaints validates the taints of the machine template the way KubeadmControlPlane does: they must
// not use the keys reserved for Cluster API, the node lifecycle and the cloud provider. The feature gate is checked
// separately, see validateMachineTaintsFeatureGate.
func validateMachineTaints(taints []clusterv1.MachineTaint, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	for i, taint := range taints {
		idxPath := fldPath.Index(i)

		switch {
		case taint.Key == clusterv1.NodeUninitializedTaint.Key, taint.Key == clusterv1.NodeOutdatedRevisionTaint.Key:
			allErrs = append(allErrs, field.Invalid(idxPath.Child("key"), taint.Key, "taint key is not allowed"))
		case strings.HasPrefix(taint.Key, "node.kubernetes.io/") && taint.Key != "node.kubernetes.io/out-of-service":
			allErrs = append(allErrs, field.Invalid(idxPath.Child("key"), taint.Key, "taint key must not have the prefix node.kubernetes.io/, except for node.kubernetes.io/out-of-service"))
		case strings.HasPrefix(taint.Key, "node.cloudprovider.kubernetes.io/"):
			allErrs = append(allErrs, field.Invalid(idxPath.Child("key"), taint.Key, "taint key must not have the prefix node.cloudprovider.kubernetes.io/"))
		case taint.Key == "node-role.kubernetes.io/master":
			allErrs = append(allErrs, field.Invalid(idxPath.Child("key"), taint.Key, "taint is deprecated since 1.24 and should not be used anymore"))
		}

		for _, msg := range content.IsLabelKey(taint.Key) {
			allErrs = append(allErrs, field.Invalid(idxPath.Child("key"), taint.Key, msg))
		}
	}

	return allErrs
}

func validateRolloutStrategy(rolloutStrategy *RolloutStrategy, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if rolloutStrategy == nil {
		return allErrs
	}

	switch rolloutStrategy.Type {
	case "":
	case RollingUpdateStrategyType:
	case OnDeleteStrategyType:
	default:
		allErrs = append(allErrs,
			field.Invalid(fldPath, rolloutStrategy.Type,
				fmt.Sprintf("valid values are: %q", []RolloutStrategyType{RollingUpdateStrategyType, OnDeleteStrategyType}),
			),
		)
	}

	return allErrs
}

// validateMachineNamingStrategy validates that strategy.Template renders successfully
// and that its output varies with .random.
//
// The .random check is heuristic: the template is rendered twice with different random
// inputs and the outputs are compared. This accepts any whitespace variant of {{ .random }}
// but will reject templates that reference .random yet always produce constant output
// (e.g. {{ if eq .random "" }}x{{ else }}x{{ end }}, {{ slice .random 0 0 }},
// {{ printf "%d" (len .random) }}). Such templates are not useful in practice since
// they would still cause name collisions across machines.
func validateMachineNamingStrategy(strategy *MachineNamingStrategy, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if strategy == nil || strategy.Template == "" {
		return allErrs
	}

	out1, err := renderTalosControlPlaneMachineName(strategy, "cluster", "talos-control-plane", "AAAAA")
	if err != nil {
		allErrs = append(allErrs,
			field.Invalid(fldPath.Child("template"), strategy.Template, err.Error()),
		)

		return allErrs
	}
	out2, err := renderTalosControlPlaneMachineName(strategy, "cluster", "talos-control-plane", "BBBBB")
	if err != nil {
		allErrs = append(allErrs,
			field.Invalid(fldPath.Child("template"), strategy.Template, err.Error()),
		)

		return allErrs
	}
	if out1 == out2 {
		allErrs = append(allErrs,
			field.Invalid(fldPath.Child("template"), strategy.Template, "must reference .random"),
		)

		return allErrs
	}

	exampleName, err := generateTalosControlPlaneMachineName(strategy, "cluster", "talos-control-plane")
	if err != nil {
		allErrs = append(allErrs,
			field.Invalid(fldPath.Child("template"), strategy.Template, err.Error()),
		)

		return allErrs
	}

	for _, msg := range validation.IsDNS1123Subdomain(exampleName) {
		allErrs = append(allErrs,
			field.Invalid(
				fldPath.Child("template"),
				strategy.Template,
				fmt.Sprintf("produces invalid machine name %q: %s", exampleName, msg),
			),
		)
	}

	return allErrs
}

func newInvalidTalosControlPlaneError(kind, name string, allErrs field.ErrorList) error {
	return apierrors.NewInvalid(
		schema.GroupKind{Group: GroupVersion.Group, Kind: kind},
		name,
		allErrs,
	)
}
