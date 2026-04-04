// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager implements webhook methods.
func (r *TalosControlPlaneTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplanetemplate,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanetemplates,versions=v1alpha3,name=default.taloscontrolplanetemplate.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1
// +kubebuilder:webhook:verbs=create;update;delete,path=/validate-controlplane-cluster-x-k8s-io-v1alpha3-taloscontrolplanetemplate,mutating=false,failurePolicy=fail,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanetemplates,versions=v1alpha3,name=validate.taloscontrolplanetemplate.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var (
	_ webhook.CustomDefaulter = &TalosControlPlaneTemplate{}
	_ webhook.CustomValidator = &TalosControlPlaneTemplate{}
)

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (r *TalosControlPlaneTemplate) Default(_ context.Context, obj runtime.Object) error {
	r = obj.(*TalosControlPlaneTemplate)

	defaultTalosControlPlaneTemplateSpec(&r.Spec, r.Namespace)

	return nil
}

func defaultTalosControlPlaneTemplateSpec(s *TalosControlPlaneTemplateSpec, namespace string) {
	s.Template.Spec.SyncInfrastructureTemplateCompatibility()
	defaultObjectReferenceNamespace(&s.Template.Spec.MachineTemplate.InfrastructureRef, namespace)
	defaultObjectReferenceNamespace(&s.Template.Spec.InfrastructureTemplate, namespace)
	s.Template.Spec.SyncInfrastructureTemplateCompatibility()

	s.Template.Spec.RolloutStrategy = defaultRolloutStrategy(s.Template.Spec.RolloutStrategy)
}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlaneTemplate) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	r = obj.(*TalosControlPlaneTemplate)

	return r.validate()
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlaneTemplate) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldTemplate, ok := oldObj.(*TalosControlPlaneTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a TalosControlPlaneTemplate but got a %T", oldObj))
	}

	newTemplate, ok := newObj.(*TalosControlPlaneTemplate)
	if !ok {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("expected a TalosControlPlaneTemplate but got a %T", newObj))
	}

	oldTemplate = oldTemplate.DeepCopy()
	newTemplate = newTemplate.DeepCopy()

	if err := r.Default(ctx, oldTemplate); err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("failed to compare old and new TalosControlPlaneTemplate: failed to default old object: %v", err))
	}
	if err := r.Default(ctx, newTemplate); err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("failed to compare old and new TalosControlPlaneTemplate: failed to default new object: %v", err))
	}

	if diff := cmp.Diff(oldTemplate.Spec.Template.Spec, newTemplate.Spec.Template.Spec); diff != "" {
		return nil, newInvalidTalosControlPlaneError("TalosControlPlaneTemplate", newTemplate.Name, field.ErrorList{
			field.Invalid(
				field.NewPath("spec", "template", "spec"),
				newTemplate.Spec.Template.Spec,
				fmt.Sprintf("field is immutable. Please create a new resource instead. Diff: %s", diff),
			),
		})
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (r *TalosControlPlaneTemplate) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosControlPlaneTemplate) validate() (admission.Warnings, error) {
	allErrs := validateInfrastructureTemplateCompatibility(
		r.Spec.Template.Spec.MachineTemplate,
		r.Spec.Template.Spec.InfrastructureTemplate,
		field.NewPath("spec", "template", "spec"),
		false,
	)
	allErrs = append(allErrs, validateMachineNamingStrategy(r.Spec.Template.Spec.MachineNamingStrategy, field.NewPath("spec", "template", "spec", "machineNamingStrategy"))...)
	allErrs = append(allErrs, validateRolloutStrategy(r.Spec.Template.Spec.RolloutStrategy, field.NewPath("spec", "template", "spec", "rolloutStrategy"))...)
	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, newInvalidTalosControlPlaneError("TalosControlPlaneTemplate", r.Name, allErrs)
}
