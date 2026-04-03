// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager implements webhook methods.
func (r *TalosControlPlane) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplane,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1alpha3,name=default.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1
//+kubebuilder:webhook:verbs=create;update;delete,path=/validate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplane,mutating=false,failurePolicy=fail,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanes,versions=v1alpha3,name=validate.taloscontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var (
	_ webhook.CustomDefaulter = &TalosControlPlane{}
	_ webhook.CustomValidator = &TalosControlPlane{}
)

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (r *TalosControlPlane) Default(_ context.Context, obj runtime.Object) error {
	r = obj.(*TalosControlPlane)

	defaultTalosControlPlaneSpec(&r.Spec, r.Namespace)

	return nil
}

func defaultTalosControlPlaneSpec(s *TalosControlPlaneSpec, namespace string) {
	if s.Replicas == nil {
		replicas := int32(1)
		s.Replicas = &replicas
	}

	s.SyncInfrastructureTemplateCompatibility()
	defaultObjectReferenceNamespace(&s.MachineTemplate.InfrastructureRef, namespace)
	defaultObjectReferenceNamespace(&s.InfrastructureTemplate, namespace)
	s.SyncInfrastructureTemplateCompatibility()

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

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlane) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	r = obj.(*TalosControlPlane)

	return r.validate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlane) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	r = newObj.(*TalosControlPlane)

	return r.validate()
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlane) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosControlPlane) validate() (admission.Warnings, error) {
	allErrs := validateInfrastructureTemplateCompatibility(
		r.Spec.MachineTemplate,
		r.Spec.InfrastructureTemplate,
		field.NewPath("spec"),
		true,
	)
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

func newInvalidTalosControlPlaneError(kind, name string, allErrs field.ErrorList) error {
	return apierrors.NewInvalid(
		schema.GroupKind{Group: GroupVersion.Group, Kind: kind},
		name,
		allErrs,
	)
}

func defaultObjectReferenceNamespace(ref *corev1.ObjectReference, namespace string) {
	if hasObjectReference(*ref) && ref.Namespace == "" {
		ref.Namespace = namespace
	}
}

func validateInfrastructureTemplateCompatibility(machineTemplate TalosControlPlaneMachineTemplate, legacy corev1.ObjectReference, fldPath *field.Path, requireRef bool) field.ErrorList {
	var allErrs field.ErrorList

	machineRefPath := fldPath.Child("machineTemplate", "infrastructureRef")
	legacyRefPath := fldPath.Child("infrastructureTemplate")

	hasMachineRef := hasObjectReference(machineTemplate.InfrastructureRef)
	hasLegacyRef := hasObjectReference(legacy)

	if requireRef && !hasMachineRef && !hasLegacyRef {
		allErrs = append(allErrs,
			field.Required(machineRefPath, "machineTemplate.infrastructureRef or infrastructureTemplate must be set"),
		)
	}

	if hasMachineRef && hasLegacyRef && machineTemplate.InfrastructureRef != legacy {
		allErrs = append(allErrs,
			field.Invalid(machineRefPath, machineTemplate.InfrastructureRef, "must match spec.infrastructureTemplate when both fields are set"),
			field.Invalid(legacyRefPath, legacy, "must match spec.machineTemplate.infrastructureRef when both fields are set"),
		)
	}

	return allErrs
}
