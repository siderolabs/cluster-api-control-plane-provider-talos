// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// TalosControlPlaneFinalizer is the finalizer used by the controller to clean up owned Machines.
	TalosControlPlaneFinalizer = "talos.controlplane.cluster.x-k8s.io/finalizer"

	// TalosControlPlaneFinalizerLegacy is kept temporarily so the controller can migrate
	// existing objects away from the old non-path-qualified finalizer value.
	TalosControlPlaneFinalizerLegacy = "talos.controlplane.cluster.x-k8s.io"
)

type ControlPlaneConfig struct {
	// Deprecated: starting from cacppt v0.4.0 provider doesn't use init configs.
	InitConfig         cabptv1.TalosConfigSpec `json:"init,omitempty"`
	ControlPlaneConfig cabptv1.TalosConfigSpec `json:"controlplane"`
}

// TalosControlPlaneMachineTemplate defines how control plane Machines should be shaped.
type TalosControlPlaneMachineTemplate struct {
	// Metadata is the standard object's metadata.
	// +optional
	Metadata clusterv1.ObjectMeta `json:"metadata,omitempty"`

	// InfrastructureRef is a reference to a custom resource offered by an infrastructure provider.
	// For ClusterClass / topology, this field is populated from ClusterClass.spec.controlPlane.machineInfrastructure.ref.
	// +optional
	InfrastructureRef corev1.ObjectReference `json:"infrastructureRef,omitempty"`

	// ReadinessGates specifies additional conditions to include when evaluating Machine Ready condition.
	// +optional
	// +listType=map
	// +listMapKey=conditionType
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	ReadinessGates []clusterv1.MachineReadinessGate `json:"readinessGates,omitempty"`

	// NodeDrainTimeout is the total amount of time that the controller will spend draining a control plane node.
	// +optional
	NodeDrainTimeout *metav1.Duration `json:"nodeDrainTimeout,omitempty"`

	// NodeVolumeDetachTimeout is the total amount of time that the controller will spend waiting for volumes to be detached.
	// +optional
	NodeVolumeDetachTimeout *metav1.Duration `json:"nodeVolumeDetachTimeout,omitempty"`

	// NodeDeletionTimeout defines how long the controller will attempt to delete the Node that is hosted by a Machine.
	// +optional
	NodeDeletionTimeout *metav1.Duration `json:"nodeDeletionTimeout,omitempty"`
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

	// MachineTemplate contains information about how control plane Machines should be shaped.
	// This is the Cluster API v1beta1 control plane contract field consumed by ClusterClass / topology.
	// +optional
	MachineTemplate TalosControlPlaneMachineTemplate `json:"machineTemplate,omitempty"`

	// InfrastructureTemplate is kept as a deprecated compatibility alias for users of older TalosControlPlane manifests.
	// New manifests should use spec.machineTemplate.infrastructureRef instead.
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

// GetReplicas reads spec replicas in a safe way.
// If replicas is nil it will return 0.
func (s *TalosControlPlaneSpec) GetReplicas() int32 {
	if s.Replicas == nil {
		return 0
	}

	return *s.Replicas
}

// SyncInfrastructureTemplateCompatibility keeps the deprecated infrastructureTemplate alias
// and the Cluster API machineTemplate.infrastructureRef field aligned when only one is set.
func (s *TalosControlPlaneSpec) SyncInfrastructureTemplateCompatibility() {
	syncInfrastructureTemplateCompatibility(&s.MachineTemplate, &s.InfrastructureTemplate)
}

// InfrastructureTemplateRef returns the resolved infrastructure machine template reference.
func (s *TalosControlPlaneSpec) InfrastructureTemplateRef() corev1.ObjectReference {
	return resolvedInfrastructureTemplateRef(s.MachineTemplate, s.InfrastructureTemplate)
}

// HasInfrastructureTemplateRef reports whether the control plane spec has a resolved infrastructure template reference.
func (s *TalosControlPlaneSpec) HasInfrastructureTemplateRef() bool {
	return hasObjectReference(s.InfrastructureTemplateRef())
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

	// Total number of unavailable machines targeted by this control plane.
	// This is the total number of machines that are still required for
	// the deployment to have 100% available capacity. They may either
	// be machines that are running but not yet ready or machines
	// that still have not been created.
	// +optional
	UnavailableReplicas int32 `json:"unavailableReplicas,omitempty"`

	// Total number of non-terminated Machines targeted by this control plane that have the desired spec.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

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
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`

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
func (r *TalosControlPlane) GetConditions() clusterv1.Conditions {
	return r.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (r *TalosControlPlane) SetConditions(conditions clusterv1.Conditions) {
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

func syncInfrastructureTemplateCompatibility(machineTemplate *TalosControlPlaneMachineTemplate, legacy *corev1.ObjectReference) {
	switch {
	case !hasObjectReference(machineTemplate.InfrastructureRef) && hasObjectReference(*legacy):
		machineTemplate.InfrastructureRef = *legacy
	case hasObjectReference(machineTemplate.InfrastructureRef) && !hasObjectReference(*legacy):
		*legacy = machineTemplate.InfrastructureRef
	}
}

func resolvedInfrastructureTemplateRef(machineTemplate TalosControlPlaneMachineTemplate, legacy corev1.ObjectReference) corev1.ObjectReference {
	if hasObjectReference(machineTemplate.InfrastructureRef) {
		return machineTemplate.InfrastructureRef
	}

	return legacy
}

func hasObjectReference(ref corev1.ObjectReference) bool {
	return ref.APIVersion != "" ||
		ref.Kind != "" ||
		ref.Name != "" ||
		ref.Namespace != "" ||
		ref.ResourceVersion != "" ||
		ref.FieldPath != "" ||
		ref.UID != ""
}
