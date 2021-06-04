// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"strings"

	"github.com/talos-systems/talos/pkg/machinery/api/machine"
	talosclient "github.com/talos-systems/talos/pkg/machinery/client"
	capiv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gracefulEtcdLeave removes a given machine from the etcd cluster by forfeiting leadership
// and issuing a "leave" request from the machine itself.
func (r *TalosControlPlaneReconciler) gracefulEtcdLeave(ctx context.Context, c *talosclient.Client, cluster client.ObjectKey, machineToLeave capiv1.Machine) error {
	r.Log.Info("Verifying etcd status", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

	svcs, err := c.ServiceInfo(ctx, "etcd")
	if err != nil {
		return err
	}

	for _, svc := range svcs {
		if svc.Service.State != "Finished" {
			r.Log.Info("Forfeiting leadership", "machine", machineToLeave.Status.NodeRef.Name)

			_, err = c.EtcdForfeitLeadership(ctx, &machine.EtcdForfeitLeadershipRequest{})
			if err != nil {
				return err
			}

			r.Log.Info("Leaving etcd", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

			err = c.EtcdLeaveCluster(ctx, &machine.EtcdLeaveClusterRequest{})
			if err != nil {
				return err
			}
		}

		break
	}

	return nil
}

// forceEtcdLeave removes a given machine from the etcd cluster by telling another CP node to remove the member.
// This is used in times when the machine was deleted out from under us.
func (r *TalosControlPlaneReconciler) forceEtcdLeave(ctx context.Context, c *talosclient.Client, cluster client.ObjectKey, memberName string) error {
	r.Log.Info("Removing etcd member", "memberName", memberName)

	err := c.EtcdRemoveMember(
		ctx,
		&machine.EtcdRemoveMemberRequest{
			Member: memberName,
		},
	)
	if err != nil {
		return err
	}

	return nil
}

// auditEtcd rolls through all etcd members to see if there's a matching controlplane machine
// It uses the first controlplane node returned as the etcd endpoint
func (r *TalosControlPlaneReconciler) auditEtcd(ctx context.Context, cluster client.ObjectKey, cpName string) error {
	machines, err := r.getControlPlaneMachinesForCluster(ctx, cluster, cpName)
	if err != nil {
		return err
	}

	if len(machines) == 0 {
		return nil
	}

	for _, machine := range machines {
		// nb: we'll assume any machine that doesn't have a noderef is new and we can audit later because
		//     otherwise a new etcd member can get removed before even getting the noderef set by the CAPI controllers.
		if machine.Status.NodeRef == nil {
			return fmt.Errorf("some CP machines do not have a noderef")
		}
	}
	// Select the first CP machine that's not being deleted and has a noderef
	var designatedCPMachine capiv1.Machine

	for _, machine := range machines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() && machine.Status.NodeRef != nil {
			designatedCPMachine = machine
			break
		}
	}

	clientset, err := r.kubeconfigForCluster(ctx, cluster)
	if err != nil {
		return err
	}

	c, err := r.talosconfigForMachine(ctx, clientset, designatedCPMachine)
	if err != nil {
		return err
	}

	// Save the first internal IP of the designated machine to use as our node target
	// and setup the ctx to target it
	var firstIntAddr string

	for _, addr := range designatedCPMachine.Status.Addresses {
		if addr.Type == capiv1.MachineInternalIP {
			firstIntAddr = addr.Address
			break
		}
	}

	nodeCtx := talosclient.WithNodes(ctx, firstIntAddr)

	response, err := c.EtcdMemberList(nodeCtx, &machine.EtcdMemberListRequest{})
	if err != nil {
		return err
	}

	// Only querying one CP node, so only 1 message should return.
	memberList := response.Messages[0]

	if len(memberList.Members) == 0 {
		return nil
	}

	// For each etcd member, look through the list of machines and see if noderef matches
	for _, member := range memberList.Members {
		present := false
		for _, machine := range machines {
			// break apart the noderef name in case it's an fqdn (like in AWS)
			machineNodeNameExploded := strings.Split(machine.Status.NodeRef.Name, ".")

			if machineNodeNameExploded[0] == member {
				present = true
				break
			}
		}

		if !present {
			r.Log.Info("found etcd member that doesn't exist as controlplane machine", "member", member)

			r.forceEtcdLeave(nodeCtx, c, cluster, member)
		}
	}

	return nil
}
