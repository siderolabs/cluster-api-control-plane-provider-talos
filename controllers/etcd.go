// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// needsRemediation reports whether a MachineHealthCheck flagged the machine as unhealthy and left its
// remediation to the control plane (MachineHealthCheckSucceeded=False and MachineOwnerRemediated=False).
// Both conditions are required: when the machine recovers on its own the MachineHealthCheck controller
// sets MachineHealthCheckSucceeded back to True but leaves MachineOwnerRemediated to the owner, so a
// check on MachineOwnerRemediated alone would treat the machine as unhealthy forever.
func needsRemediation(machine *clusterv1.Machine) bool {
	return collections.IsUnhealthyAndOwnerRemediated(machine)
}

// remainingMachines returns the owned machines that are staying in the control plane: those that are
// not being deleted, not leaving etcd (see etcdLeavingAnnotation), and not flagged for remediation by
// a MachineHealthCheck (see needsRemediation). A machine in any of these states is on its way out, so
// its stopped or already-removed etcd must not keep EtcdClusterHealthyCondition false and deadlock
// scale-down, rollout, and remediation. For the same reason the boot check before a scale-down only
// waits for these machines.
func remainingMachines(ownedMachines []clusterv1.Machine) []clusterv1.Machine {
	machines := make([]clusterv1.Machine, 0, len(ownedMachines))

	for _, machine := range ownedMachines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() &&
			machine.Annotations[etcdLeavingAnnotation] != "true" &&
			!needsRemediation(&machine) {
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

// etcdMemberNames returns the set of host names (see nodeNamesForMachine) an etcd member may carry to
// belong to one of the machines. Machines without a noderef have no known names and contribute nothing.
func etcdMemberNames(machines []clusterv1.Machine) map[string]struct{} {
	names := map[string]struct{}{}

	for _, machine := range machines {
		if machine.Status.NodeRef == nil {
			continue
		}

		for _, name := range nodeNamesForMachine(machine) {
			names[name] = struct{}{}
		}
	}

	return names
}

// etcdMembership holds what an etcd member list is verified against: the host names of the machines
// that must be etcd members (expected, keyed by machine name), the machines that must be members but
// whose names are not known yet because they have no noderef (pending), and the host names of every
// owned machine (owned), which decide whether a member is an orphan.
type etcdMembership struct {
	expected map[string][]string
	pending  []string
	owned    map[string]struct{}
}

// newEtcdMembership builds the membership for the owned machines, of which machines is the subset
// that takes part in the etcd health check (see remainingMachines).
func newEtcdMembership(ownedMachines, machines []clusterv1.Machine) etcdMembership {
	membership := etcdMembership{
		expected: make(map[string][]string, len(machines)),
		owned:    etcdMemberNames(ownedMachines),
	}

	for _, machine := range machines {
		if machine.Status.NodeRef == nil {
			membership.pending = append(membership.pending, machine.Name)

			continue
		}

		membership.expected[machine.Name] = nodeNamesForMachine(machine)
	}

	return membership
}

// verify checks the etcd member list reported by node and returns the set of member host names,
// lowercased, for comparison across nodes.
//
// Every machine that must be a member has to have one, matched by any of its host names. A machine
// that must be a member but has no noderef yet (it is new and the kubelet has not registered) cannot
// be matched by name, so it is accounted for by count: each such machine claims one member matching
// no owned machine. Any further unmatched member is an orphan (auditEtcd force-removes these), and
// fewer unmatched members than pending machines means a new machine has not joined etcd yet. A member
// of an excluded machine still matches an owned machine, so it is tolerated; that machine's member
// may also already be gone, which is expected, so it is not required.
func (m etcdMembership) verify(node string, members []*machineapi.EtcdMember) (map[string]struct{}, error) {
	present := make(map[string]struct{}, len(members))

	var unmatched []string

	for _, member := range members {
		name := strings.ToLower(member.Hostname)
		present[name] = struct{}{}

		if _, ok := m.owned[name]; !ok {
			unmatched = append(unmatched, member.Hostname)
		}
	}

	for machineName, names := range m.expected {
		if !slices.ContainsFunc(names, func(name string) bool {
			_, ok := present[name]

			return ok
		}) {
			return nil, fmt.Errorf("%s: etcd is missing a member for control plane machine %q", node, machineName)
		}
	}

	switch {
	case len(unmatched) > len(m.pending):
		return nil, fmt.Errorf("%s: etcd member %q does not match any control plane machine", node, unmatched[len(m.pending)])
	case len(unmatched) < len(m.pending):
		return nil, fmt.Errorf("%s: etcd is missing a member for %d of the control plane machines without a noderef yet %q",
			node, len(m.pending)-len(unmatched), m.pending)
	}

	return present, nil
}

// compareEtcdMembers reports a member list that differs from the one another node reported: each etcd
// member must see the same cluster, so a member known to only one of the two nodes is an error.
func compareEtcdMembers(node string, members map[string]struct{}, previousNode string, previous map[string]struct{}) error {
	for name := range members {
		if _, ok := previous[name]; !ok {
			return fmt.Errorf("%s: etcd member %q is not known to %s", node, name, previousNode)
		}
	}

	for name := range previous {
		if _, ok := members[name]; !ok {
			return fmt.Errorf("%s: etcd member %q known to %s is missing", node, name, previousNode)
		}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) etcdHealthcheck(ctx context.Context, tcp *controlplanev1.TalosControlPlane, ownedMachines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	machines := remainingMachines(ownedMachines)

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

	probes := make([]etcdMachineProbe, len(machines))

	var wg sync.WaitGroup

	for i, machine := range machines {
		wg.Go(func() {
			probes[i] = r.probeEtcd(ctx, tcp, machine)
		})
	}

	wg.Wait()

	health := etcdHealth{
		machines: len(machines),
		statuses: make(map[string]etcdMachineStatus, len(machines)),
	}

	// the member list reported by the previous node, to check that every node sees the same cluster
	var (
		previousNode    string
		previousMembers map[string]struct{}
	)

	for _, machine := range machines {
		// loop for each machine, the client created has endpoints which point to a single machine
		if err := func() error {
			if probe.inspectErr != nil {
				return probe.inspectErr
			}

			health.inspected++

			// check that etcd service is healthy on the node
			if err := etcdServiceError(probe.services); err != nil {
				return err
			}

			health.healthy++

			status := etcdMachineStatus{healthy: true, listErr: probe.listErr}

			if probe.listErr == nil && len(probe.memberList.GetMessages()) > 0 {
				// The client points to a single machine, so only 1 message should return.
				status.members = probe.memberList.Messages[0].Members
			}

			health.statuses[machine.Name] = status

			if probe.listErr != nil {
				return probe.listErr
			}

			for _, message := range resp.Messages {
				node := message.Metadata.GetHostname()

				members, err := membership.verify(node, message.Members)
				if err != nil {
					return err
				}

				if previousMembers != nil {
					if err := compareEtcdMembers(node, members, previousNode, previousMembers); err != nil {
						return err
					}
				}

				previousNode, previousMembers = node, members
			}

			return nil
		}(); err != nil {
			return fmt.Errorf("error checking etcd health on machine %q: %w", machine.Name, err)
		}
	}

	return nil
}

// etcdMemberMatchesMachine reports whether the etcd member runs on the machine. It matches the member hostname
// against the nodeRef name and any MachineHostName addresses: some infrastructure providers report the
// infrastructure machine name as MachineHostName, which may not match the actual node hostname.
func etcdMemberMatchesMachine(member *machineapi.EtcdMember, machine *clusterv1.Machine) bool {
	candidates := []string{machine.Status.NodeRef.Name}

	for _, address := range machine.Status.Addresses {
		if address.Type == clusterv1.MachineHostName {
			candidates = append(candidates, address.Address)
		}
	}

	for _, hostname := range candidates {
		if hostname == "" {
			continue
		}

		// break apart the hostname in case it is an fqdn (like in AWS)
		hostname, _, _ = strings.Cut(hostname, ".")
		if strings.EqualFold(hostname, member.Hostname) {
			return true
		}
	}

	return false
}

// findEtcdMember returns the etcd member running on the machine, or nil if there is none.
func findEtcdMember(members []*machineapi.EtcdMember, machine *clusterv1.Machine) *machineapi.EtcdMember {
	for _, member := range members {
		if etcdMemberMatchesMachine(member, machine) {
			return member
		}
	}

	return nil
}

// etcdMemberManager runs the etcd operations that remediation and scale down need on individual machines.
// The default implementation goes through the Talos API of the machines; tests replace it.
type etcdMemberManager interface {
	// ServiceHealth returns an error unless the etcd service on the machine is running and healthy.
	ServiceHealth(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error
	// MemberList returns the etcd member list as seen by the machine.
	MemberList(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) ([]*machineapi.EtcdMember, error)
	// Leave forfeits etcd leadership and leaves the etcd cluster from the machine itself. It fails if the etcd service
	// is not running on the machine.
	Leave(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error
	// RemoveMember removes the etcd member through another machine.
	RemoveMember(ctx context.Context, tcp *controlplanev1.TalosControlPlane, via *clusterv1.Machine, member *machineapi.EtcdMember) error
}

// etcdMembers returns the etcdMemberManager of the reconciler.
func (r *TalosControlPlaneReconciler) etcdMembers() etcdMemberManager {
	if r.etcd != nil {
		return r.etcd
	}

	return talosEtcdMemberManager{r: r}
}

// talosEtcdMemberManager implements etcdMemberManager through the Talos API.
type talosEtcdMemberManager struct {
	r *TalosControlPlaneReconciler
}

func (m talosEtcdMemberManager) ServiceHealth(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	c, err := m.r.talosconfigForMachines(ctx, tcp, *machine)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	svcs, err := c.ServiceInfo(ctx, etcdService)
	if err != nil {
		return err
	}

	return etcdServiceError(svcs)
}

func (m talosEtcdMemberManager) MemberList(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) ([]*machineapi.EtcdMember, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	c, err := m.r.talosconfigForMachines(ctx, tcp, *machine)
	if err != nil {
		return nil, err
	}

	defer c.Close() //nolint:errcheck

	resp, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
	if err != nil {
		return nil, err
	}

	// The client points to a single machine, so only 1 message should return.
	if len(resp.Messages) == 0 {
		return nil, fmt.Errorf("empty etcd member list response from machine %q", machine.Name)
	}

	return resp.Messages[0].Members, nil
}

func (m talosEtcdMemberManager) Leave(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error {
	c, err := m.r.talosconfigForMachines(ctx, tcp, *machine)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	return m.r.gracefulEtcdLeave(ctx, c, *machine)
}

func (m talosEtcdMemberManager) RemoveMember(ctx context.Context, tcp *controlplanev1.TalosControlPlane, via *clusterv1.Machine, member *machineapi.EtcdMember) error {
	c, err := m.r.talosconfigForMachines(ctx, tcp, *via)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	return m.r.forceEtcdLeave(ctx, c, member)
}

// removeMachineFromEtcd marks the machine as leaving etcd and removes its etcd member, so that the machine can be
// deleted. It first asks the machine to leave etcd gracefully, which also forfeits leadership. If that fails, e.g.
// because the machine is unreachable, it removes the member through another control plane machine.
//
// If the member could not be removed either way, an error is returned: the caller must not delete the machine then,
// because its member would stay in the etcd cluster. The etcd-leaving mark is removed again if this call set it; a
// mark set before is kept, since the member may have left etcd then.
func (r *TalosControlPlaneReconciler) removeMachineFromEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) error {
	_, alreadyLeaving := machine.Annotations[etcdLeavingAnnotation]

	// Mark machine as leaving etcd so health check skips it even if reconciliation
	// crashes between leaving etcd and deleting the machine (prevents deadlock where
	// stopped etcd without DeletionTimestamp fails health checks forever).
	if err := r.setEtcdLeavingAnnotation(ctx, machine, true); err != nil {
		return err
	}

	leaveErr := r.etcdMemberLeave(ctx, tcp, machine, machines)
	if leaveErr == nil || alreadyLeaving {
		return leaveErr
	}

	if err := r.setEtcdLeavingAnnotation(ctx, machine, false); err != nil {
		r.Log.Error(err, "failed to remove the etcd-leaving annotation", "machine", machine.Name)
	}

	return leaveErr
}

func (r *TalosControlPlaneReconciler) setEtcdLeavingAnnotation(ctx context.Context, machine *clusterv1.Machine, leaving bool) error {
	if _, ok := machine.Annotations[etcdLeavingAnnotation]; ok == leaving {
		return nil
	}

	patchHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return err
	}

	if leaving {
		if machine.Annotations == nil {
			machine.Annotations = map[string]string{}
		}

		machine.Annotations[etcdLeavingAnnotation] = "true"
	} else {
		delete(machine.Annotations, etcdLeavingAnnotation)
	}

	return patchHelper.Patch(ctx, machine)
}

func (r *TalosControlPlaneReconciler) etcdMemberLeave(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) error {
	etcd := r.etcdMembers()

	leaveErr := etcd.Leave(ctx, tcp, machine)
	if leaveErr == nil {
		return nil
	}

	r.Log.Info("graceful etcd leave failed, removing the etcd member through another control plane machine", "machine", machine.Name, "error", leaveErr)

	errs := []error{fmt.Errorf("graceful leave: %w", leaveErr)}

	for other := range otherEtcdMachines(machine, machines) {
		members, err := etcd.MemberList(ctx, tcp, other)
		if err != nil {
			errs = append(errs, fmt.Errorf("listing members via %q: %w", other.Name, err))

			continue
		}

		member := findEtcdMember(members, machine)
		if member == nil {
			r.Log.Info("machine is not an etcd member anymore", "machine", machine.Name, "via", other.Name)

			return nil
		}

		if err := etcd.RemoveMember(ctx, tcp, other, member); err != nil {
			errs = append(errs, fmt.Errorf("removing member via %q: %w", other.Name, err))

			continue
		}

		return nil
	}

	return fmt.Errorf("failed to remove machine %q from etcd: %w", machine.Name, errors.Join(errs...))
}

// otherEtcdMachines returns the control plane machines other than the given machine that etcd can be reached through:
// they have a node, and are neither being deleted nor leaving etcd.
func otherEtcdMachines(machine *clusterv1.Machine, machines []*clusterv1.Machine) iter.Seq[*clusterv1.Machine] {
	return func(yield func(*clusterv1.Machine) bool) {
		for _, other := range machines {
			if other.Name == machine.Name ||
				!other.DeletionTimestamp.IsZero() ||
				!other.Status.NodeRef.IsDefined() ||
				other.Annotations[etcdLeavingAnnotation] == "true" {
				continue
			}

			if !yield(other) {
				return
			}
		}
	}
}

// etcdMembersVia returns the etcd member list as seen by one of the otherEtcdMachines of the given machine. ok is
// false if there is no such machine.
func (r *TalosControlPlaneReconciler) etcdMembersVia(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) (members []*machineapi.EtcdMember, ok bool, err error) {
	var errs []error

	for other := range otherEtcdMachines(machine, machines) {
		members, err := r.etcdMembers().MemberList(ctx, tcp, other)
		if err != nil {
			errs = append(errs, fmt.Errorf("listing members via %q: %w", other.Name, err))

			continue
		}

		return members, true, nil
	}

	return nil, len(errs) > 0, errors.Join(errs...)
}

// gracefulEtcdLeave removes a given machine from the etcd cluster by forfeiting leadership
// and issuing a "leave" request from the machine itself.
//
// It fails if the etcd service is not running on the machine: whether its member already left etcd or not, it
// can't be told from the machine itself, so the caller must check, and remove the member, through another machine.
func (r *TalosControlPlaneReconciler) gracefulEtcdLeave(ctx context.Context, c *talosclient.Client, machineToLeave clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	defer cancel()

	r.Log.Info("verifying etcd status", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

	svcs, err := c.ServiceInfo(ctx, etcdService)
	if err != nil {
		return err
	}

	if len(svcs) == 0 {
		return fmt.Errorf("service %q not found on machine %q", etcdService, machineToLeave.Name)
	}

	// The client points to a single machine, so only 1 service should return.
	for _, svc := range svcs {
		if state := svc.Service.GetState(); state != "Running" {
			return fmt.Errorf("service %q is not running on machine %q: current state %q", etcdService, machineToLeave.Name, state)
		}
	}

	r.Log.Info("forfeiting leadership", "machine", machineToLeave.Status.NodeRef.Name)

	if _, err = c.EtcdForfeitLeadership(ctx, &machineapi.EtcdForfeitLeadershipRequest{}); err != nil {
		return err
	}

	r.Log.Info("leaving etcd", "machine", machineToLeave.Name, "node", machineToLeave.Status.NodeRef.Name)

	return c.EtcdLeaveCluster(ctx, &machineapi.EtcdLeaveClusterRequest{})
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
		if !machine.Status.NodeRef.IsDefined() {
			return fmt.Errorf("some CP machines do not have a noderef")
		}
	}
	// Select the first CP machine that's not being deleted and has a noderef
	var designatedCPMachine clusterv1.Machine

	for _, machine := range machines.Items {
		if !machine.ObjectMeta.DeletionTimestamp.IsZero() || !machine.Status.NodeRef.IsDefined() {
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

	// every member must carry a host name of one of the machines, matched the same way the health check does
	owned := etcdMemberNames(machines.Items)

	for _, member := range memberList.Members {
		if member.Hostname == "" {
			return fmt.Errorf("discovered etcd member with empty hostname: %s", member)
		}

		_, present := owned[strings.ToLower(member.Hostname)]

		if !present {
			r.Log.Info("found etcd member that doesn't exist as controlplane machine", "member", member)

			if err = r.forceEtcdLeave(ctx, c, member); err != nil {
				return fmt.Errorf("error leaving etcd for member %q via machine %q", member, designatedCPMachine.Name)
			}
		}
	}

	return nil
}
