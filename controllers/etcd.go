// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// machinesForEtcdHealthcheck returns the owned machines that should take part in the etcd health
// check: those that are not being deleted, not leaving etcd (see etcdLeavingAnnotation), and not
// flagged for remediation by a MachineHealthCheck (MachineOwnerRemediated=False). A machine in any
// of these states is on its way out, so its stopped or already-removed etcd must not keep
// EtcdClusterHealthyCondition false and deadlock scale-down, rollout, and remediation.
func machinesForEtcdHealthcheck(ownedMachines []clusterv1.Machine) []clusterv1.Machine {
	machines := make([]clusterv1.Machine, 0, len(ownedMachines))

	for _, machine := range ownedMachines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() &&
			machine.Annotations[etcdLeavingAnnotation] != "true" &&
			!conditions.IsFalse(&machine, clusterv1.MachineOwnerRemediatedCondition) {
			machines = append(machines, machine)
		}
	}

	return machines
}

// nodeNamesForMachine returns the host names that can identify a machine's etcd member: the noderef
// name and every MachineHostName address (some infrastructure providers report the infrastructure
// machine name there, which may not match the node hostname). Each name is lowercased and has any
// domain suffix trimmed (noderef names can be FQDNs, e.g. on AWS). The caller must ensure NodeRef is set.
func nodeNamesForMachine(machine clusterv1.Machine) []string {
	candidates := []string{machine.Status.NodeRef.Name}

	for _, address := range machine.Status.Addresses {
		if address.Type == clusterv1.MachineHostName {
			candidates = append(candidates, address.Address)
		}
	}

	names := make([]string, 0, len(candidates))

	for _, hostname := range candidates {
		hostname, _, _ = strings.Cut(hostname, ".")
		names = append(names, strings.ToLower(hostname))
	}

	return names
}

// etcdMembership holds the host names an etcd member list is verified against: those of the machines
// that must be etcd members (expected, keyed by machine name) and those of every owned machine
// (owned). Each machine contributes all of its nodeNamesForMachine candidates.
type etcdMembership struct {
	expected       map[string][]string
	owned          map[string]struct{}
	allNodeRefsSet bool
}

// newEtcdMembership builds the membership for the owned machines, of which machines is the subset
// that takes part in the etcd health check (see machinesForEtcdHealthcheck).
func newEtcdMembership(ownedMachines, machines []clusterv1.Machine) etcdMembership {
	membership := etcdMembership{
		expected:       make(map[string][]string, len(machines)),
		owned:          make(map[string]struct{}, len(ownedMachines)),
		allNodeRefsSet: true,
	}

	for _, machine := range ownedMachines {
		if machine.Status.NodeRef == nil {
			membership.allNodeRefsSet = false

			continue
		}

		for _, name := range nodeNamesForMachine(machine) {
			membership.owned[name] = struct{}{}
		}
	}

	for _, machine := range machines {
		if machine.Status.NodeRef == nil {
			continue
		}

		membership.expected[machine.Name] = nodeNamesForMachine(machine)
	}

	return membership
}

// verify checks the etcd member list reported by node. A member matching no owned machine is an
// orphan (auditEtcd force-removes these); a member of an excluded machine still matches an owned
// machine, so it is tolerated. The orphan check is skipped while a machine still lacks a noderef: it
// is new and gets matched on a later pass, the same assumption auditEtcd makes. Every machine that
// must be a member has to have one, matched by any of its host names; an excluded machine's member
// may already be gone, which is expected, so those are not required.
func (m etcdMembership) verify(node string, members []*machineapi.EtcdMember) error {
	present := make(map[string]struct{}, len(members))

	for _, member := range members {
		name := strings.ToLower(member.Hostname)
		present[name] = struct{}{}

		if m.allNodeRefsSet {
			if _, ok := m.owned[name]; !ok {
				return fmt.Errorf("%s: etcd member %q does not match any control plane machine", node, member.Hostname)
			}
		}
	}

	for machineName, names := range m.expected {
		if !slices.ContainsFunc(names, func(name string) bool {
			_, ok := present[name]

			return ok
		}) {
			return fmt.Errorf("%s: etcd is missing a member for control plane machine %q", node, machineName)
		}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) etcdHealthcheck(ctx context.Context, tcp *controlplanev1.TalosControlPlane, ownedMachines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	machines := machinesForEtcdHealthcheck(ownedMachines)

	// If every owned machine is on its way out (deleting, leaving, or being remediated) there is
	// nothing left to verify. Reporting etcd healthy here would open the scale-down/rollout gate
	// without a single member checked, so treat it as unhealthy until a machine is back in the set.
	if len(ownedMachines) > 0 && len(machines) == 0 {
		return fmt.Errorf("all %d owned control plane machines are excluded from the etcd health check", len(ownedMachines))
	}

	membership := newEtcdMembership(ownedMachines, machines)

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
				node := message.Metadata.GetHostname()

				for _, member := range message.Members {
					// check that the member list is the same on all nodes
					if _, found := members[member.Hostname]; i > 0 && !found {
						return fmt.Errorf("%s: found extra etcd member %s", node, member.Hostname)
					}

					members[member.Hostname] = struct{}{}
				}

				if err := membership.verify(node, message.Members); err != nil {
					return err
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
			if slices.Contains(nodeNamesForMachine(machine), strings.ToLower(member.Hostname)) {
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
