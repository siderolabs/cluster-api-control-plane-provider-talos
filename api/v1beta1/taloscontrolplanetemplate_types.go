// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// TalosControlPlaneTemplateSpec defines the desired state of TalosControlPlaneTemplate.
type TalosControlPlaneTemplateSpec struct {
	Template TalosControlPlaneTemplateResource `json:"template"`
}

// TalosControlPlaneTemplateResource describes the data needed to create a TalosControlPlane from a template.
type TalosControlPlaneTemplateResource struct {
	// Standard object's metadata.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/metadata/
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	Spec TalosControlPlaneTemplateResourceSpec `json:"spec"`
}

// TalosControlPlaneTemplateResourceSpec defines the desired state of TalosControlPlane through a template.
type TalosControlPlaneTemplateResourceSpec struct {
	// MachineTemplate contains information about how control plane Machines should be shaped.
	// For ClusterClass / topology, ClusterClass.spec.controlPlane.machineInfrastructure.templateRef
	// populates machineTemplate.spec.infrastructureRef when creating a concrete TalosControlPlane.
	// The template variant intentionally omits infrastructureRef and readinessGates: those fields
	// are owned by the topology controller (infrastructureRef) or by individual TalosControlPlane
	// instances (readinessGates).
	// +optional
	MachineTemplate TalosControlPlaneTemplateMachineTemplate `json:"machineTemplate,omitempty,omitzero"`

	// MachineNamingStrategy allows changing the naming pattern used when creating control plane Machines.
	// InfraMachines and bootstrap configs use the same name as the corresponding Machine.
	// +optional
	MachineNamingStrategy *MachineNamingStrategy `json:"machineNamingStrategy,omitempty"`

	// ControlPlaneConfig is a two TalosConfigSpecs
	// to use for initializing and joining machines to the control plane.
	ControlPlaneConfig ControlPlaneConfig `json:"controlPlaneConfig"`

	// The RolloutStrategy to use to replace control plane machines with
	// new ones.
	// +optional
	// +kubebuilder:default={type: "RollingUpdate", rollingUpdate: {maxSurge: 1}}
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`
}

// TalosControlPlaneTemplateMachineTemplate is the MachineTemplate carried by a
// TalosControlPlaneTemplate. It mirrors the upstream KubeadmControlPlaneTemplate variant by
// omitting the infrastructure reference and readiness gates: those fields are populated either
// by the topology controller (infrastructureRef) or by the concrete TalosControlPlane instance
// (readinessGates).
type TalosControlPlaneTemplateMachineTemplate struct {
	// ObjectMeta is the standard object's metadata.
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// Spec is the specification of the desired behavior of the machine template.
	// +optional
	Spec TalosControlPlaneTemplateMachineTemplateSpec `json:"spec,omitempty,omitzero"`
}

// TalosControlPlaneTemplateMachineTemplateSpec carries the per-template Machine spec fields.
// +kubebuilder:validation:MinProperties=1
type TalosControlPlaneTemplateMachineTemplateSpec struct {
	// Deletion contains configuration options for Machine deletion.
	// +optional
	Deletion TalosControlPlaneMachineTemplateDeletionSpec `json:"deletion,omitempty,omitzero"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=taloscontrolplanetemplates,shortName=tcpt,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TalosControlPlaneTemplate is the Schema for the taloscontrolplanetemplates API.
type TalosControlPlaneTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec TalosControlPlaneTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// TalosControlPlaneTemplateList contains a list of TalosControlPlaneTemplate.
type TalosControlPlaneTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosControlPlaneTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosControlPlaneTemplate{}, &TalosControlPlaneTemplateList{})
}
