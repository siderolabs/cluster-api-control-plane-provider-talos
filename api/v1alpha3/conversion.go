// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	controlplanev1beta2 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta2"
)

// ConvertTo converts v1alpha3 TalosControlPlane to the hub version (v1beta2).
func (src *TalosControlPlane) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*controlplanev1beta2.TalosControlPlane)

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.Version = src.Spec.Version
	dst.Spec.InfrastructureTemplate = src.Spec.InfrastructureTemplate
	dst.Spec.MinReadySeconds = src.Spec.MinReadySeconds

	dst.Spec.ControlPlaneConfig = controlplanev1beta2.ControlPlaneConfig{
		ControlPlaneConfig: src.Spec.ControlPlaneConfig.ControlPlaneConfig,
		InitConfig:         src.Spec.ControlPlaneConfig.InitConfig,
	}

	if src.Spec.RolloutStrategy != nil {
		dst.Spec.RolloutStrategy = &controlplanev1beta2.RolloutStrategy{
			Type: controlplanev1beta2.RolloutStrategyType(src.Spec.RolloutStrategy.Type),
		}
		if src.Spec.RolloutStrategy.RollingUpdate != nil {
			dst.Spec.RolloutStrategy.RollingUpdate = &controlplanev1beta2.RollingUpdate{
				MaxSurge: src.Spec.RolloutStrategy.RollingUpdate.MaxSurge,
			}
		}
	}

	// Status
	dst.Status.Selector = src.Status.Selector
	dst.Status.Replicas = src.Status.Replicas
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.AvailableReplicas = src.Status.AvailableReplicas
	dst.Status.UpToDateReplicas = src.Status.UpToDateReplicas
	dst.Status.UnavailableReplicas = src.Status.UnavailableReplicas
	dst.Status.Initialized = src.Status.Initialized
	dst.Status.Ready = src.Status.Ready
	dst.Status.Bootstrapped = src.Status.Bootstrapped
	dst.Status.FailureReason = src.Status.FailureReason
	dst.Status.FailureMessage = src.Status.FailureMessage
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	dst.Status.V1Beta2 = (*controlplanev1beta2.TalosControlPlaneV1Beta2Status)(nil)
	if src.Status.V1Beta2 != nil {
		dst.Status.V1Beta2 = &controlplanev1beta2.TalosControlPlaneV1Beta2Status{
			ReadyReplicas:     src.Status.V1Beta2.ReadyReplicas,
			AvailableReplicas: src.Status.V1Beta2.AvailableReplicas,
			UpToDateReplicas:  src.Status.V1Beta2.UpToDateReplicas,
			Conditions:        src.Status.V1Beta2.Conditions,
		}
	}
	dst.Status.Version = src.Status.Version

	return nil
}

// ConvertFrom converts from the hub version (v1beta2) to v1alpha3.
func (dst *TalosControlPlane) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*controlplanev1beta2.TalosControlPlane)

	dst.ObjectMeta = src.ObjectMeta

	// Spec
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.Version = src.Spec.Version
	dst.Spec.InfrastructureTemplate = src.Spec.InfrastructureTemplate
	dst.Spec.MinReadySeconds = src.Spec.MinReadySeconds

	dst.Spec.ControlPlaneConfig = ControlPlaneConfig{
		ControlPlaneConfig: src.Spec.ControlPlaneConfig.ControlPlaneConfig,
		InitConfig:         src.Spec.ControlPlaneConfig.InitConfig,
	}

	if src.Spec.RolloutStrategy != nil {
		dst.Spec.RolloutStrategy = &RolloutStrategy{
			Type: RolloutStrategyType(src.Spec.RolloutStrategy.Type),
		}
		if src.Spec.RolloutStrategy.RollingUpdate != nil {
			dst.Spec.RolloutStrategy.RollingUpdate = &RollingUpdate{
				MaxSurge: src.Spec.RolloutStrategy.RollingUpdate.MaxSurge,
			}
		}
	}

	// Status
	dst.Status.Selector = src.Status.Selector
	dst.Status.Replicas = src.Status.Replicas
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.AvailableReplicas = src.Status.AvailableReplicas
	dst.Status.UpToDateReplicas = src.Status.UpToDateReplicas
	dst.Status.UnavailableReplicas = src.Status.UnavailableReplicas
	dst.Status.Initialized = src.Status.Initialized
	dst.Status.Ready = src.Status.Ready
	dst.Status.Bootstrapped = src.Status.Bootstrapped
	dst.Status.FailureReason = src.Status.FailureReason
	dst.Status.FailureMessage = src.Status.FailureMessage
	dst.Status.ObservedGeneration = src.Status.ObservedGeneration
	dst.Status.Conditions = src.Status.Conditions
	if src.Status.V1Beta2 != nil {
		dst.Status.V1Beta2 = &TalosControlPlaneV1Beta2Status{
			ReadyReplicas:     src.Status.V1Beta2.ReadyReplicas,
			AvailableReplicas: src.Status.V1Beta2.AvailableReplicas,
			UpToDateReplicas:  src.Status.V1Beta2.UpToDateReplicas,
			Conditions:        src.Status.V1Beta2.Conditions,
		}
	}
	dst.Status.Version = src.Status.Version

	return nil
}
