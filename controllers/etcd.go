// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *TalosControlPlaneReconciler) etcdHealthcheck(ctx context.Context, tcp *controlplanev1.TalosControlPlane, ownedMachines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	machines := []clusterv1.Machine{}

	for _, machine := range ownedMachines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() {
			machines = append(machines, machine)
		}
	}

	params := make([]any, 0, len(machines)*2)
	for _, machine := range machines {
		params = append(params, "node", machine.Name)
	}

	r.Log.Info("verifying etcd health on all nodes", params...)

	const service = "etcd"

	// list of discovered etcd members, updated on each iteration
	members := map[string]struct{}{}

	for i, machine := range machines {
		// loop for each machine, the client created has endpoints which point to a single machine
		if err := func() error {
			c, err := r.talosconfigForMachines(ctx, tcp, machine)
			if err != nil {
				return err
			}

			defer c.Close() //nolint:errcheck

			svcs, err := c.ServiceInfo(ctx, service)
			if err != nil {
				return err
			}

			// check that etcd service is healthy on the node
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

			resp, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
			if err != nil {
				return err
			}

			for _, message := range resp.Messages {
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
		}(); err != nil {
			return fmt.Errorf("error checking etcd health on machine %q: %w", machines[i].Name, err)
		}
	}

	return nil
}

// gracefulEtcdLeave removes a given machine from the etcd cluster by forfeiting leadership
// and issuing a "leave" request from the machine itself.
func (r *TalosControlPlaneReconciler) gracefulEtcdLeave(ctx context.Context, c *talosclient.Client, machineToLeave clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	defer cancel()

	r.Log.Info("verifying etcd status", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

	svcs, err := c.ServiceInfo(ctx, "etcd")
	if err != nil {
		return err
	}

	for _, svc := range svcs {
		if svc.Service.State != "Finished" {
			r.Log.Info("forfeiting leadership", "machine", machineToLeave.Status.NodeRef.Name)

			_, err = c.EtcdForfeitLeadership(ctx, &machineapi.EtcdForfeitLeadershipRequest{})
			if err != nil {
				return err
			}

			r.Log.Info("leaving etcd", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

			err = c.EtcdLeaveCluster(ctx, &machineapi.EtcdLeaveClusterRequest{})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// forceEtcdLeave removes a given machine from the etcd cluster by telling another CP node to remove the member.
// This is used in times when the machine was deleted out from under us.
func (r *TalosControlPlaneReconciler) forceEtcdLeave(ctx context.Context, c *talosclient.Client, member *machineapi.EtcdMember) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	defer cancel()

	r.Log.Info("removing etcd member", "memberName", member.Hostname, "memberId", member.Id)

	return c.EtcdRemoveMemberByID(
		ctx,
		&machineapi.EtcdRemoveMemberByIDRequest{
			MemberId: member.Id,
		},
	)
}

// auditEtcd rolls through all etcd members to see if there's a matching controlplane machine
// It uses the first controlplane node returned as the etcd endpoint
func (r *TalosControlPlaneReconciler) auditEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster client.ObjectKey) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	defer cancel()

	machines, err := r.getControlPlaneMachinesForCluster(ctx, cluster)
	if err != nil {
		return err
	}

	if len(machines.Items) == 0 {
		return nil
	}

	for _, machine := range machines.Items {
		// nb: we'll assume any machine that doesn't have a noderef is new and we can audit later because
		//     otherwise a new etcd member can get removed before even getting the noderef set by the CAPI controllers.
		if machine.Status.NodeRef == nil {
			return fmt.Errorf("some CP machines do not have a noderef")
		}
	}
	// Select the first CP machine that's not being deleted and has a noderef
	var designatedCPMachine clusterv1.Machine

	for _, machine := range machines.Items {
		if !machine.ObjectMeta.DeletionTimestamp.IsZero() || machine.Status.NodeRef == nil {
			continue
		}

		designatedCPMachine = machine

		break
	}

	if designatedCPMachine.Name == "" {
		return fmt.Errorf("no CP machine which is not being deleted and has node ref")
	}

	c, err := r.talosconfigForMachines(ctx, tcp, designatedCPMachine)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	response, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
	if err != nil {
		return fmt.Errorf("error getting etcd members via %q (endpoints %v): %w", designatedCPMachine.Name, c.GetConfigContext().Endpoints, err)
	}

	// Only querying one CP node, so only 1 message should return.
	memberList := response.Messages[0]

	// For each etcd member, look through the list of machines and see if noderef matches
	for _, member := range memberList.Members {
		if member.Hostname == "" {
			return fmt.Errorf("discovered etcd member with empty hostname: %s", member)
		}

		present := false
		for _, machine := range machines.Items {
			hostname := machine.Status.NodeRef.Name

			for _, address := range machine.Status.Addresses {
				if address.Type == clusterv1.MachineHostName {
					hostname = address.Address

					break
				}
			}

			// break apart the noderef name in case it's an fqdn (like in AWS)
			hostname, _, _ = strings.Cut(hostname, ".")

			if strings.EqualFold(hostname, member.Hostname) {
				present = true

				break
			}
		}

		if !present {
			r.Log.Info("found etcd member that doesn't exist as controlplane machine", "member", member)

			if err = r.forceEtcdLeave(ctx, c, member); err != nil {
				return fmt.Errorf("error leaving etcd for member %q via machine %q", member, designatedCPMachine.Name)
			}
		}
	}

	return nil
}
