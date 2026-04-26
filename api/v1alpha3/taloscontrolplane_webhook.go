// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
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

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplane,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1alpha3,name=default.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1
//+kubebuilder:webhook:verbs=create;update;delete,path=/validate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplane,mutating=false,failurePolicy=fail,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1alpha3,name=validate.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

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
	return obj.validate()
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateUpdate(_ context.Context, _, newObj *TalosControlPlane) (admission.Warnings, error) {
	return newObj.validate()
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlane) ValidateDelete(_ context.Context, _ *TalosControlPlane) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosControlPlane) validate() (admission.Warnings, error) {
	allErrs := field.ErrorList{}

	if r.Spec.MachineTemplate.Spec.InfrastructureRef.Name == "" {
		allErrs = append(allErrs,
			field.Required(
				field.NewPath("spec", "machineTemplate", "spec", "infrastructureRef"),
				"infrastructureRef is required",
			),
		)
	}

	allErrs = append(allErrs, validateMachineNamingStrategy(r.Spec.MachineNamingStrategy, field.NewPath("spec", "machineNamingStrategy"))...)
	allErrs = append(allErrs, validateRolloutStrategy(r.Spec.RolloutStrategy, field.NewPath("spec", "rolloutStrategy"))...)
	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, newInvalidTalosControlPlaneError("TalosControlPlane", r.Name, allErrs)
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

func validateMachineNamingStrategy(strategy *MachineNamingStrategy, fldPath *field.Path) field.ErrorList {
	var allErrs field.ErrorList

	if strategy == nil || strategy.Template == "" {
		return allErrs
	}

	if !strings.Contains(strategy.Template, "{{ .random }}") {
		allErrs = append(allErrs,
			field.Invalid(fldPath.Child("template"), strategy.Template, "must contain {{ .random }}"),
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
