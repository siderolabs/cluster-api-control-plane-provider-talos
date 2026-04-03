// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
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
	Metadata clusterv1.ObjectMeta `json:"metadata,omitempty"`

	Spec TalosControlPlaneTemplateResourceSpec `json:"spec"`
}

// TalosControlPlaneTemplateResourceSpec defines the desired state of TalosControlPlane through a template.
type TalosControlPlaneTemplateResourceSpec struct {
	// MachineTemplate contains information about how control plane Machines should be shaped.
	// For ClusterClass / topology, ClusterClass.spec.controlPlane.machineInfrastructure.ref populates
	// machineTemplate.infrastructureRef when creating a concrete TalosControlPlane.
	// +optional
	MachineTemplate TalosControlPlaneMachineTemplate `json:"machineTemplate,omitempty"`

	// InfrastructureTemplate is kept as a deprecated compatibility alias for older template manifests.
	// New manifests should use spec.template.spec.machineTemplate.infrastructureRef when an inline
	// infrastructure reference is required outside of ClusterClass-managed topology.
	// +optional
	InfrastructureTemplate corev1.ObjectReference `json:"infrastructureTemplate,omitempty"`

	// ControlPlaneConfig is a two TalosConfigSpecs
	// to use for initializing and joining machines to the control plane.
	ControlPlaneConfig ControlPlaneConfig `json:"controlPlaneConfig"`

	// The RolloutStrategy to use to replace control plane machines with
	// new ones.
	// +optional
	// +kubebuilder:default={type: "RollingUpdate", rollingUpdate: {maxSurge: 1}}
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`
}

// SyncInfrastructureTemplateCompatibility keeps the deprecated infrastructureTemplate alias
// and the Cluster API machineTemplate.infrastructureRef field aligned when only one is set.
func (s *TalosControlPlaneTemplateResourceSpec) SyncInfrastructureTemplateCompatibility() {
	syncInfrastructureTemplateCompatibility(&s.MachineTemplate, &s.InfrastructureTemplate)
}

// InfrastructureTemplateRef returns the resolved infrastructure machine template reference.
func (s *TalosControlPlaneTemplateResourceSpec) InfrastructureTemplateRef() corev1.ObjectReference {
	return resolvedInfrastructureTemplateRef(s.MachineTemplate, s.InfrastructureTemplate)
}

// HasInfrastructureTemplateRef reports whether the template spec has a resolved infrastructure template reference.
func (s *TalosControlPlaneTemplateResourceSpec) HasInfrastructureTemplateRef() bool {
	return hasObjectReference(s.InfrastructureTemplateRef())
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
