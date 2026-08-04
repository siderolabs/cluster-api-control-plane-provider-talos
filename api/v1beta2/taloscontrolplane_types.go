// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta2

import (
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	TalosControlPlaneFinalizer = "talos.controlplane.cluster.x-k8s.io"
	SpecHashAnnotation         = "controlplane.cluster.x-k8s.io/spec-hash"
)

type ControlPlaneConfig struct {
	InitConfig         cabptv1.TalosConfigSpec `json:"init,omitempty"`
	ControlPlaneConfig cabptv1.TalosConfigSpec `json:"controlplane"`
}

type RolloutStrategyType string

const (
	RollingUpdateStrategyType RolloutStrategyType = "RollingUpdate"
	OnDeleteStrategyType      RolloutStrategyType = "OnDelete"
)

type TalosControlPlaneSpec struct {
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// +kubebuilder:validation:MinLength:=2
	// +kubebuilder:validation:Pattern:=^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-0-9a-zA-Z_\.+]*)?$
	Version                string                 `json:"version"`
	InfrastructureTemplate corev1.ObjectReference `json:"infrastructureTemplate"`
	ControlPlaneConfig     ControlPlaneConfig     `json:"controlPlaneConfig"`
	// +optional
	// +kubebuilder:default={type: "RollingUpdate", rollingUpdate: {maxSurge: 1}}
	RolloutStrategy *RolloutStrategy `json:"rolloutStrategy,omitempty"`
	// +optional
	MinReadySeconds *int32 `json:"minReadySeconds,omitempty"`
}

func (s *TalosControlPlaneSpec) GetReplicas() int32 {
	if s.Replicas == nil {
		return 0
	}
	return *s.Replicas
}

type RolloutStrategy struct {
	// +optional
	RollingUpdate *RollingUpdate `json:"rollingUpdate,omitempty"`
	// +optional
	Type RolloutStrategyType `json:"type,omitempty"`
}

type RollingUpdate struct {
	// +optional
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`
}

type TalosControlPlaneV1Beta2Status struct {
	ReadyReplicas     *int32             `json:"readyReplicas,omitempty"`
	AvailableReplicas *int32             `json:"availableReplicas,omitempty"`
	UpToDateReplicas  *int32             `json:"upToDateReplicas,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
}

type TalosControlPlaneStatus struct {
	// +optional
	Selector string `json:"selector,omitempty"`
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	AvailableReplicas *int32 `json:"availableReplicas,omitempty"`
	// +optional
	UpToDateReplicas *int32 `json:"upToDateReplicas,omitempty"`
	// +optional
	UnavailableReplicas int32 `json:"unavailableReplicas,omitempty"`
	// +optional
	Initialized bool `json:"initialized"`
	// +optional
	Ready bool `json:"ready"`
	// +optional
	Bootstrapped bool `json:"bootstrapped,omitempty"`
	// +optional
	FailureReason *string `json:"failureReason,omitempty"`
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	V1Beta2 *TalosControlPlaneV1Beta2Status `json:"v1beta2,omitempty"`
	// +optional
	Version *string `json:"version,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=taloscontrolplanes,shortName=tcp,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Initialized",type=boolean,JSONPath=".status.initialized"
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Ready Replicas",type=integer,JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Unavailable Replicas",type=integer,JSONPath=".status.unavailableReplicas"

type TalosControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TalosControlPlaneSpec   `json:"spec,omitempty"`
	Status            TalosControlPlaneStatus `json:"status,omitempty"`
}

func (r *TalosControlPlane) GetConditions() []metav1.Condition {
	return r.Status.Conditions
}

func (r *TalosControlPlane) SetConditions(conditions []metav1.Condition) {
	r.Status.Conditions = conditions
}

// Hub marks v1beta2 as the conversion hub.
func (*TalosControlPlane) Hub() {}

// +kubebuilder:object:root=true

type TalosControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TalosControlPlane{}, &TalosControlPlaneList{})
}
