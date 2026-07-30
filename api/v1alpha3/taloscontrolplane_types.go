// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	TalosControlPlaneFinalizer = "talos.controlplane.cluster.x-k8s.io"

	SpecHashAnnotation = "controlplane.cluster.x-k8s.io/spec-hash"
)

type ControlPlaneConfig struct {
	// Deprecated: starting from cacppt v0.4.0 provider doesn't use init configs.
	InitConfig         cabptv1.TalosConfigSpec `json:"init,omitempty"`
	ControlPlaneConfig cabptv1.TalosConfigSpec `json:"controlplane"`
}

// RolloutStrategyType defines the rollout strategies for a KubeadmControlPlane.
type RolloutStrategyType string

const (
	// RollingUpdateStrategyType replaces the old control planes by new one using rolling update
	// i.e. gradually scale up or down the old control planes and scale up or down the new one.
	RollingUpdateStrategyType RolloutStrategyType = "RollingUpdate"
	// OnDeleteStrategyType doesn't replace the nodes automatically, but if the machine is removed,
	// new one will be created from the new spec.
	OnDeleteStrategyType RolloutStrategyType = "OnDelete"
)

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

	// The RolloutStrategy to use to replace control plane machines with
	// new ones.
	// +optional
	// +kubebuilder:default={type: "RollingUpdate", rollingUpdate: {maxSurge: 1}}
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`

	// MinReadySeconds is the minimum number of seconds a machine must be Ready
	// before it is considered available.
	// +optional
	MinReadySeconds *int32 `json:"minReadySeconds,omitempty"`
}

// TalosControlPlaneV1Beta2Status holds the v1beta2 contract fields required by Cluster API
type TalosControlPlaneV1Beta2Status struct {
	// ReadyReplicas is the number of machines with a Ready Condition
	ReadyReplicas *int32 `json:"readyReplicas,omitempty"`

	// AvailableReplicas is the number of machines that are both Ready and have passed
	// MinReadySeconds threshold. This counts machines actively serving requests
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`

	// UpToDateReplicas is the number of machines whose configuration matches the current
	// TalosControlPlane spec. Machines with mismatched hashes are undergoing rollout
	UpToDateReplicas *int32 `json:"upToDateReplicas,omitempty"`

	// Conditions represent the observations of the TalosControlPlane's state
	// Uses metav1.Condition (not clusterv1.Condition) per v1beta2 contract
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GetReplicas reads spec replicas in a safe way.
// If replicas is nil it will return 0.
func (s *TalosControlPlaneSpec) GetReplicas() int32 {
	if s.Replicas == nil {
		return 0
	}

	return *s.Replicas
}

// RolloutStrategy describes how to replace existing machines
// with new ones.
type RolloutStrategy struct {
	// Rolling update config params. Present only if
	// RolloutStrategyType = RollingUpdate.
	// +optional
	RollingUpdate *RollingUpdate `json:"rollingUpdate,omitempty"`

	// Change rollout strategy.
	//
	// Supported strategies:
	//  * "RollingUpdate".
	//  * "OnDelete"
	//
	// Default is RollingUpdate.
	// +optional
	Type RolloutStrategyType `json:"type,omitempty"`
}

// RollingUpdate is used to control the desired behavior of rolling update.
type RollingUpdate struct {
	// The maximum number of control planes that can be scheduled above or under the
	// desired number of control planes.
	// Value can be an absolute number 1 or 0.
	// Defaults to 1.
	// Example: when this is set to 1, the control plane can be scaled
	// up immediately when the rolling update starts.
	// +optional
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`
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

	// availableReplicas is the number of available replicas for this ControlPlane. A machine is considered available when Machine's Available condition is true.
	// +optional
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`

	// upToDateReplicas is the number of up-to-date replicas targeted by this ControlPlane. A machine is considered available when Machine's  UpToDate condition is true.
	// +optional
	UpToDateReplicas *int32 `json:"upToDateReplicas,omitempty"`

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

	// Bootstrapped denotes whether any nodes received bootstrap request
	// which is required to start etcd and Kubernetes components in Talos.
	// +optional
	Bootstrapped bool `json:"bootstrapped,omitempty"`

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
	Conditions []metav1.Condition `json:"conditions,omitempty"`


	// V1Beta2 holds fields required by the v1beta2 CAPI contract
	// +optional
	V1Beta2 *TalosControlPlaneV1Beta2Status `json:"v1beta2,omitempty"`

	// version represents the minimum Kubernetes version for the control plane machines
	// in the cluster.
	// +optional
	Version *string `json:"version,omitempty"`
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
func (r *TalosControlPlane) GetConditions() []metav1.Condition {
	return r.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (r *TalosControlPlane) SetConditions(conditions []metav1.Condition) {
	r.Status.Conditions = conditions
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
