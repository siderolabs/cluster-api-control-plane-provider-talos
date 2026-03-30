// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"

	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

func (r *TalosControlPlaneReconciler) upgradeControlPlane(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	controlPlane *ControlPlane,
	machinesRequireUpgrade collections.Machines,
) (ctrl.Result, error) {
	logger := controlPlane.Logger()

	// Before proceeding with the rolling update strategy, attempt to filter out
	// machines that can be updated in-place via the CanUpdateMachine runtime hook.
	// Machines that can be updated in-place are marked with the UpdateInProgressAnnotation
	// and removed from the rolling rollout set.
	machinesForRolling, err := r.filterMachinesForInPlaceUpdate(ctx, controlPlane, machinesRequireUpgrade)
	if err != nil {
		logger.Error(err, "error during in-place update filtering, continuing with rolling update for all machines")
		machinesForRolling = machinesRequireUpgrade
	}

	// If all machines were handled by in-place updates, no rolling update is needed.
	if machinesForRolling.Len() == 0 {
		logger.Info("all machines scheduled for in-place update, no rolling update needed")
		return ctrl.Result{}, nil
	}

	if tcp.Spec.RolloutStrategy == nil || tcp.Spec.RolloutStrategy.RollingUpdate == nil {
		return ctrl.Result{}, errors.New("rolloutStrategy is not set")
	}

	switch tcp.Spec.RolloutStrategy.Type {
	case controlplanev1.RollingUpdateStrategyType:
		// RolloutStrategy is currently defaulted and validated to be RollingUpdate
		maxNodes := tcp.Spec.GetReplicas() + int32(tcp.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue())
		if int32(controlPlane.Machines.Len()) < maxNodes {
			// scaleUp ensures that we don't continue scaling up while waiting for Machines to have NodeRefs
			return r.scaleUpControlPlane(ctx, cluster, tcp, controlPlane)
		}

		return r.scaleDownControlPlane(ctx, cluster, tcp, controlPlane, machinesForRolling)
	case controlplanev1.OnDeleteStrategyType:
		// nothing to do, scale up handler will take care of creating machines with the new spec
		return ctrl.Result{}, nil
	default:
		logger.Info("RolloutStrategy type is not set to RollingUpdateStrategyType, unable to determine the strategy for rolling out machines")
		return ctrl.Result{}, nil
	}
}
