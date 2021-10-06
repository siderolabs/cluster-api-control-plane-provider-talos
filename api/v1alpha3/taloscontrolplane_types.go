// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	cabptv1 "github.com/talos-systems/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
)

const (
	TalosControlPlaneFinalizer = "talos.controlplane.cluster.x-k8s.io"
)

type ControlPlaneConfig struct {
	InitConfig         cabptv1.TalosConfigSpec `json:"init"`
	ControlPlaneConfig cabptv1.TalosConfigSpec `json:"controlplane"`
}

// TalosControlPlaneSpec defines the desired state of TalosControlPlane
type TalosControlPlaneSpec struct {
	// Number of desired machines. Defaults to 1. When stacked etcd is used only
	// odd numbers are permitted, as per [etcd best practice](https://etcd.io/docs/v3.3.12/faq/#why-an-odd-number-of-cluster-members).
	// This is a pointer to distinguish between explicit zero and not specified.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Version defines the desired Kubernetes version.
	// +kubebuilder:validation:MinLength:=2
	// +kubebuilder:validation:Pattern:=^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-0-9a-zA-Z_\.+]*)?$
	Version string `json:"version"`

	// InfrastructureTemplate is a required reference to a custom resource
	// offered by an infrastructure provider.
	InfrastructureTemplate corev1.ObjectReference `json:"infrastructureTemplate"`

	// ControlPlaneConfig is a two TalosConfigSpecs
	// to use for initializing and joining machines to the control plane.
	ControlPlaneConfig ControlPlaneConfig `json:"controlPlaneConfig"`
}

// TalosControlPlaneStatus defines the observed state of TalosControlPlane
type TalosControlPlaneStatus struct {
	// Selector is the label selector in string format to avoid introspection
	// by clients, and is used to provide the CRD-based integration for the
	// scale subresource and additional integrations for things like kubectl
	// describe.. The string will be in the same format as the query-param syntax.
	// More info about label selectors: http://kubernetes.io/docs/user-guide/labels#label-selectors
	// +optional
	Selector string `json:"selector,omitempty"`

	// Total number of non-terminated machines targeted by this control plane
	// (their labels match the selector).
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Total number of fully running and ready control plane machines.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Total number of unavailable machines targeted by this control plane.
	// This is the total number of machines that are still required for
	// the deployment to have 100% available capacity. They may either
	// be machines that are running but not yet ready or machines
	// that still have not been created.
	// +optional
	UnavailableReplicas int32 `json:"unavailableReplicas,omitempty"`

	// Initialized denotes whether or not the control plane has the
	// uploaded talos-config configmap.
	// +optional
	Initialized bool `json:"initialized"`

	// Ready denotes that the TalosControlPlane API Server is ready to
	// receive requests.
	// +optional
	Ready bool `json:"ready"`

	// FailureReason indicates that there is a terminal problem reconciling the
	// state, and will be set to a token value suitable for
	// programmatic interpretation.
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`

	// ErrorMessage indicates that there is a terminal problem reconciling the
	// state, and will be set to a descriptive error message.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`

	// ObservedGeneration is the latest generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions defines current service state of the KubeadmControlPlane.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=taloscontrolplanes,shortName=tcp,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready",description="TalosControlPlane API Server is ready to receive requests"
// +kubebuilder:printcolumn:name="Initialized",type=boolean,JSONPath=".status.initialized",description="This denotes whether or not the control plane has the uploaded talos-config configmap"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.replicas",description="Total number of non-terminated machines targeted by this control plane"
// +kubebuilder:printcolumn:name="Ready Replicas",type=integer,JSONPath=".status.readyReplicas",description="Total number of fully running and ready control plane machines"
// +kubebuilder:printcolumn:name="Unavailable Replicas",type=integer,JSONPath=".status.unavailableReplicas",description="Total number of unavailable machines targeted by this control plane"

// TalosControlPlane is the Schema for the taloscontrolplanes API
type TalosControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosControlPlaneSpec   `json:"spec,omitempty"`
	Status TalosControlPlaneStatus `json:"status,omitempty"`
}

// GetConditions returns the set of conditions for this object.
func (in *TalosControlPlane) GetConditions() clusterv1.Conditions {
	return in.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (in *TalosControlPlane) SetConditions(conditions clusterv1.Conditions) {
	in.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// TalosControlPlaneList contains a list of TalosControlPlane
type TalosControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosControlPlane{}, &TalosControlPlaneList{})
}
