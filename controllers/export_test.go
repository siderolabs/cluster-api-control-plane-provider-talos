// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

// EtcdHealthcheckReason runs the etcd health check and returns the reason the EtcdClusterHealthy condition gets
// when the health check fails.
func (r *TalosControlPlaneReconciler) EtcdHealthcheckReason(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) (string, error) {
	health, err := r.etcdHealthcheck(ctx, tcp, machines)

	return health.conditionReason(), err
}

// RemoveMachineFromEtcd removes the etcd member of the machine through the Talos API, as remediation and scale down do.
func (r *TalosControlPlaneReconciler) RemoveMachineFromEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []clusterv1.Machine) error {
	ptrs := make([]*clusterv1.Machine, 0, len(machines))
	for i := range machines {
		ptrs = append(ptrs, &machines[i])
	}

	return r.removeMachineFromEtcd(ctx, tcp, machine, ptrs)
}
