// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager implements webhook methods.
func (r *TalosControlPlaneTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1beta1-taloscontrolplanetemplate,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanetemplates,versions=v1beta1,name=default.taloscontrolplanetemplate.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1
// +kubebuilder:webhook:verbs=create;update;delete,path=/validate-controlplane-cluster-x-k8s-io-v1beta1-taloscontrolplanetemplate,mutating=false,failurePolicy=fail,groups=controlplane.cluster.x-k8s.io,resources=taloscontrolplanetemplates,versions=v1beta1,name=validate.taloscontrolplanetemplate.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1

var (
	_ admission.Defaulter[*TalosControlPlaneTemplate] = &TalosControlPlaneTemplate{}
	_ admission.Validator[*TalosControlPlaneTemplate] = &TalosControlPlaneTemplate{}
)

// Default implements admission.Defaulter so a webhook will be registered for the type.
func (*TalosControlPlaneTemplate) Default(_ context.Context, obj *TalosControlPlaneTemplate) error {
	defaultTalosControlPlaneTemplateSpec(&obj.Spec)

	return nil
}

func defaultTalosControlPlaneTemplateSpec(s *TalosControlPlaneTemplateSpec) {
	s.Template.Spec.RolloutStrategy = defaultRolloutStrategy(s.Template.Spec.RolloutStrategy)
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlaneTemplate) ValidateCreate(_ context.Context, obj *TalosControlPlaneTemplate) (admission.Warnings, error) {
	return obj.validate()
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type.
func (r *TalosControlPlaneTemplate) ValidateUpdate(ctx context.Context, oldObj, newObj *TalosControlPlaneTemplate) (admission.Warnings, error) {
	oldTemplate := oldObj.DeepCopy()
	newTemplate := newObj.DeepCopy()

	if err := r.Default(ctx, oldTemplate); err != nil {
		return nil, fmt.Errorf("failed to compare old and new TalosControlPlaneTemplate: failed to default old object: %w", err)
	}
	if err := r.Default(ctx, newTemplate); err != nil {
		return nil, fmt.Errorf("failed to compare old and new TalosControlPlaneTemplate: failed to default new object: %w", err)
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

// ValidateDelete implements admission.Validator so a webhook will be registered for the type.
func (*TalosControlPlaneTemplate) ValidateDelete(_ context.Context, _ *TalosControlPlaneTemplate) (admission.Warnings, error) {
	return nil, nil
}

func (r *TalosControlPlaneTemplate) validate() (admission.Warnings, error) {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, validateMachineNamingStrategy(r.Spec.Template.Spec.MachineNamingStrategy, field.NewPath("spec", "template", "spec", "machineNamingStrategy"))...)
	allErrs = append(allErrs, validateRolloutStrategy(r.Spec.Template.Spec.RolloutStrategy, field.NewPath("spec", "template", "spec", "rolloutStrategy"))...)
	if len(allErrs) == 0 {
		return nil, nil
	}

	return nil, newInvalidTalosControlPlaneError("TalosControlPlaneTemplate", r.Name, allErrs)
}
