// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// EtcdHealthcheckReason runs the etcd health check and returns the reason the EtcdClusterHealthy condition gets
// when the health check fails.
func (r *TalosControlPlaneReconciler) EtcdHealthcheckReason(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) (string, error) {
	health, err := r.etcdHealthcheck(ctx, tcp, machines)

	return health.conditionReason(), err
}

// EtcdHealthcheckStatuses runs the etcd health check and returns, for each machine, whether its etcd service was
// observed healthy and how many etcd members it listed.
func (r *TalosControlPlaneReconciler) EtcdHealthcheckStatuses(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) (healthy map[string]bool, members map[string]int, err error) {
	health, err := r.etcdHealthcheck(ctx, tcp, machines)

	healthy = map[string]bool{}
	members = map[string]int{}

	for name, status := range health.statuses {
		healthy[name] = status.healthy
		members[name] = len(status.members)
	}

	return healthy, members, err
}

// RemoveMachineFromEtcd removes the etcd member of the machine through the Talos API, as remediation and scale down do.
func (r *TalosControlPlaneReconciler) RemoveMachineFromEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []clusterv1.Machine) error {
	ptrs := make([]*clusterv1.Machine, 0, len(machines))
	for i := range machines {
		ptrs = append(ptrs, &machines[i])
	}

	return r.removeMachineFromEtcd(ctx, tcp, machine, ptrs)
}

// AuditEtcd removes the etcd members that run on no control plane machine of the cluster.
func (r *TalosControlPlaneReconciler) AuditEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster client.ObjectKey) error {
	return r.auditEtcd(ctx, tcp, cluster)
}

// EtcdHealthcheckAndRecordMemberIDs runs the etcd health check and records the etcd member ID each machine reported,
// as a reconcile does.
func (r *TalosControlPlaneReconciler) EtcdHealthcheckAndRecordMemberIDs(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) (healthErr, recordErr error) {
	health, healthErr := r.etcdHealthcheck(ctx, tcp, machines)

	return healthErr, r.recordEtcdMemberIDs(ctx, machines, health.statuses)
}

// EtcdMemberIDAnnotation is the annotation that records the etcd member ID of a control plane Machine.
const EtcdMemberIDAnnotation = etcdMemberIDAnnotation
