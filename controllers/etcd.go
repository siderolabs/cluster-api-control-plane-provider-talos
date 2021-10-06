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
	capiv1 "sigs.k8s.io/cluster-api/api/v1alpha4"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *TalosControlPlaneReconciler) etcdHealthcheck(ctx context.Context, cluster *capiv1.Cluster, ownedMachines []capiv1.Machine) error {
	kubeclient, err := r.kubeconfigForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		return err
	}

	defer kubeclient.Close() //nolint:errcheck

	machines := []capiv1.Machine{}

	for _, machine := range ownedMachines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() {
			machines = append(machines, machine)
		}
	}

	c, err := r.talosconfigForMachines(ctx, kubeclient.Clientset, machines...)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	service := "etcd"

	params := make([]interface{}, 0, len(machines)*2)
	for _, machine := range machines {
		params = append(params, "node", machine.Name)
	}

	r.Log.Info("Verifying etcd health on all nodes", params...)

	svcs, err := c.ServiceInfo(ctx, service)
	if err != nil {
		return err
	}

	// check that etcd service is healthy on all nodes
	for _, svc := range svcs {
		node := svc.Metadata.GetHostname()

		if len(svc.Service.Events.Events) == 0 {
			return fmt.Errorf("%s: no events recorded yet for service %q", node, service)
		}

		lastEvent := svc.Service.Events.Events[len(svc.Service.Events.Events)-1]
		if lastEvent.State != "Running" {
			return fmt.Errorf("%s: service %q not in expected state %q: current state [%s] %s", node, service, "Running", lastEvent.State, lastEvent.Msg)
		}

		if !svc.Service.GetHealth().GetHealthy() {
			return fmt.Errorf("%s: service is not healthy: %s", node, service)
		}
	}

	resp, err := c.EtcdMemberList(ctx, &machine.EtcdMemberListRequest{})
	if err != nil {
		return err
	}

	members := map[string]struct{}{}

	for i, message := range resp.Messages {
		actualMembers := len(message.Members)
		expectedMembers := len(machines)

		node := message.Metadata.GetHostname()

		// check that the count of members is the same on all nodes
		if actualMembers != expectedMembers {
			return fmt.Errorf("%s: expected to have %d members, got %d", node, expectedMembers, actualMembers)
		}

		// check that member list is the same on all nodes
		for _, member := range message.Members {
			if _, found := members[member.Hostname]; i > 0 && !found {
				return fmt.Errorf("%s: found extra etcd member %s", node, member.Hostname)
			}

			members[member.Hostname] = struct{}{}
		}
	}

	return nil
}

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
	}

	return nil
}

// forceEtcdLeave removes a given machine from the etcd cluster by telling another CP node to remove the member.
// This is used in times when the machine was deleted out from under us.
func (r *TalosControlPlaneReconciler) forceEtcdLeave(ctx context.Context, c *talosclient.Client, cluster client.ObjectKey, memberName string) error {
	r.Log.Info("Removing etcd member", "memberName", memberName)

	return c.EtcdRemoveMember(
		ctx,
		&machine.EtcdRemoveMemberRequest{
			Member: memberName,
		},
	)
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
		if !machine.ObjectMeta.DeletionTimestamp.IsZero() || machine.Status.NodeRef == nil {
			continue
		}

		designatedCPMachine = machine

		break
	}

	if designatedCPMachine.Name == "" {
		return fmt.Errorf("no CP machine which is not being deleted and has node ref")
	}

	kubeclient, err := r.kubeconfigForCluster(ctx, cluster)
	if err != nil {
		return err
	}

	defer kubeclient.Close() //nolint:errcheck

	c, err := r.talosconfigForMachines(ctx, kubeclient.Clientset, designatedCPMachine)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	response, err := c.EtcdMemberList(ctx, &machine.EtcdMemberListRequest{})
	if err != nil {
		return fmt.Errorf("error getting etcd members via %q (endpoints %v): %w", designatedCPMachine.Name, c.GetConfigContext().Endpoints, err)
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

			if machineNodeNameExploded[0] == member.Hostname {
				present = true
				break
			}
		}

		if !present {
			r.Log.Info("found etcd member that doesn't exist as controlplane machine", "member", member)

			if err = r.forceEtcdLeave(ctx, c, cluster, member.Hostname); err != nil {
				return fmt.Errorf("error leaving etcd for member %q via machine %q", member, designatedCPMachine.Name)
			}
		}
	}

	return nil
}
