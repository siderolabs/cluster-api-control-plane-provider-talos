// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"strings"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	"google.golang.org/grpc"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// etcdCalls is the subset of the Talos machine API the etcd cleanup paths use.
// *talosclient.Client satisfies it; tests substitute a fake through
// TalosControlPlaneReconciler.etcdDialer.
type etcdCalls interface {
	ServiceInfo(ctx context.Context, id string, callOptions ...grpc.CallOption) ([]talosclient.ServiceInfo, error)
	EtcdMemberList(ctx context.Context, req *machineapi.EtcdMemberListRequest, callOptions ...grpc.CallOption) (*machineapi.EtcdMemberListResponse, error)
	EtcdForfeitLeadership(ctx context.Context, req *machineapi.EtcdForfeitLeadershipRequest, callOptions ...grpc.CallOption) (*machineapi.EtcdForfeitLeadershipResponse, error)
	EtcdLeaveCluster(ctx context.Context, req *machineapi.EtcdLeaveClusterRequest, callOptions ...grpc.CallOption) error
	EtcdRemoveMemberByID(ctx context.Context, req *machineapi.EtcdRemoveMemberByIDRequest, callOptions ...grpc.CallOption) error
	Close() error
}

// etcdClientFor opens a Talos client scoped to the given machines. Endpoints come from the
// Machines' status addresses, so nothing here knows about any infrastructure provider.
func (r *TalosControlPlaneReconciler) etcdClientFor(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines ...clusterv1.Machine) (etcdCalls, error) {
	if r.etcdDialer != nil {
		return r.etcdDialer(ctx, tcp, machines...)
	}

	return r.talosconfigForMachines(ctx, tcp, machines...)
}

// machineHostName returns the hostname etcd is expected to report for a machine: the NodeRef
// name, overridden by a MachineHostName status address, cut down from an FQDN.
func machineHostName(machine *clusterv1.Machine) string {
	hostname := machine.Status.NodeRef.Name

	for _, address := range machine.Status.Addresses {
		if address.Type == clusterv1.MachineHostName {
			hostname = address.Address

			break
		}
	}

	// break apart the noderef name in case it's an fqdn (like in AWS)
	hostname, _, _ = strings.Cut(hostname, ".")

	return hostname
}
