package v1alpha3

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// SetupWebhookWithManager implements webhook methods.
func (r *TalosControlPlane) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:verbs=create;update,path=/mutate-controlplane-cluster-x-k8s-io-v1beta1-kubeadmcontrolplane,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=controlplane.cluster.x-k8s.io,resources=kubeadmcontrolplanes,versions=v1beta1,name=default.kubeadmcontrolplane.controlplane.cluster.x-k8s.io,sideEffects=None,admissionReviewVersions=v1;v1beta1

var _ webhook.Defaulter = &TalosControlPlane{}

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (r *TalosControlPlane) Default() {
	defaultTalosControlPlaneSpec(&r.Spec, r.Namespace)
}

func defaultTalosControlPlaneSpec(s *TalosControlPlaneSpec, namespace string) {
	if s.Replicas == nil {
		replicas := int32(1)
		s.Replicas = &replicas
	}

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
