// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
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
	if rolloutStrategy != nil {
		if len(rolloutStrategy.Type) == 0 {
			rolloutStrategy.Type = RollingUpdateStrategyType
		}
		if rolloutStrategy.Type == RollingUpdateStrategyType {
			if rolloutStrategy.RollingUpdate == nil {
				rolloutStrategy.RollingUpdate = &RollingUpdate{}
			}
			rolloutStrategy.RollingUpdate.MaxSurge = intstr.ValueOrDefault(rolloutStrategy.RollingUpdate.MaxSurge, ios1)
		}
	}

	return rolloutStrategy
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateCreate(_ context.Context, obj *TalosControlPlane) (admission.Warnings, error) {
	return nil, obj.toErr(obj.validate())
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateUpdate(_ context.Context, oldObj, newObj *TalosControlPlane) (admission.Warnings, error) {
	allErrs := validateInfrastructureRefImmutable(
		oldObj.Spec.MachineTemplate.Spec.InfrastructureRef,
		newObj.Spec.MachineTemplate.Spec.InfrastructureRef,
		field.NewPath("spec", "machineTemplate", "spec", "infrastructureRef"),
	)
	allErrs = append(allErrs, newObj.validate()...)

	return nil, newObj.toErr(allErrs)
}

// validateInfrastructureRefImmutable enforces that the infrastructure provider (apiGroup + kind)
// cannot change after creation; swapping providers mid-rollout would orphan in-flight machines.
// Name remains mutable so users can repoint to a re-templated resource of the same kind.
func validateInfrastructureRefImmutable(oldRef, newRef clusterv1.ContractVersionedObjectReference, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	if oldRef.APIGroup != newRef.APIGroup {
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("apiGroup"), "field is immutable"))
	}
	if oldRef.Kind != newRef.Kind {
		allErrs = append(allErrs, field.Forbidden(fldPath.Child("kind"), "field is immutable"))
	}
	return allErrs
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
