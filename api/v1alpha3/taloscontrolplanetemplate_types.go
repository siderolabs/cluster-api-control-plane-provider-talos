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
	// InfrastructureTemplate is a required reference to a custom resource
	// offered by an infrastructure provider.
	InfrastructureTemplate corev1.ObjectReference `json:"infrastructureTemplate"`

	// ControlPlaneConfig is a two TalosConfigSpecs
	// to use for initializing and joining machines to the control plane.
	ControlPlaneConfig ControlPlaneConfig `json:"controlPlaneConfig"`

	// The RolloutStrategy to use to replace control plane machines with
	// new ones.
	// +optional
	// +kubebuilder:default={type: "RollingUpdate", rollingUpdate: {maxSurge: 1}}
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`
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
