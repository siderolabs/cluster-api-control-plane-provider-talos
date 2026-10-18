// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// etcdHealth summarizes what etcdHealthcheck observed, so that callers can tell
// a degraded etcd cluster that still has quorum from one that lost it.
type etcdHealth struct {
	// machines is the number of machines that are expected to run an etcd member.
	machines int
	// inspected is the number of machines whose etcd service status could be retrieved.
	inspected int
	// healthy is the number of machines running a healthy etcd service that is a voting member.
	healthy int
	// members is the number of voting members in the etcd member list, or 0 if no member list could be retrieved.
	// Learners don't vote, so they don't count towards quorum.
	members int
}

// quorum returns the number of healthy voting members etcd needs to keep quorum.
func (h etcdHealth) quorum() int {
	total := h.members
	if total == 0 {
		total = h.machines
	}

	return total/2 + 1
}

// hasQuorum reports whether enough etcd members are healthy for etcd to keep quorum.
func (h etcdHealth) hasQuorum() bool {
	return h.healthy >= h.quorum()
}

// mayHaveQuorum reports whether etcd may still have quorum, assuming that the machines that could not be
// inspected run a healthy etcd member. When it is false, etcd lost quorum whatever the state of those machines.
func (h etcdHealth) mayHaveQuorum() bool {
	return h.healthy+(h.machines-h.inspected) >= h.quorum()
}

// conditionReason returns the reason of the EtcdClusterHealthy condition when the healthcheck failed.
func (h etcdHealth) conditionReason() string {
	switch {
	case h.inspected == 0:
		return controlplanev1.EtcdClusterInspectionFailedReason
	case h.hasQuorum():
		return controlplanev1.EtcdClusterUnhealthyReason
	case h.mayHaveQuorum():
		// Whether etcd has quorum depends on machines that could not be inspected.
		return controlplanev1.EtcdClusterInspectionFailedReason
	default:
		return controlplanev1.EtcdClusterQuorumLostReason
	}
}

// etcdHealthcheck checks etcd on every control plane machine that is not being deleted or leaving etcd.
//
// It returns the first problem found as an error, but always inspects all machines so that the returned
// etcdHealth reflects the whole cluster. Each machine gets its own timeout, so that an unreachable machine
// doesn't prevent the others from being inspected.
func (r *TalosControlPlaneReconciler) etcdHealthcheck(ctx context.Context, tcp *controlplanev1.TalosControlPlane, ownedMachines []clusterv1.Machine) (etcdHealth, error) {
	machines := []clusterv1.Machine{}

	for _, machine := range ownedMachines {
		if machine.ObjectMeta.DeletionTimestamp.IsZero() &&
			machine.Annotations[etcdLeavingAnnotation] != "true" {
			machines = append(machines, machine)
		}
	}

	params := make([]any, 0, len(machines)*2)
	for _, machine := range machines {
		params = append(params, "node", machine.Name)
	}

	r.Log.Info("verifying etcd health on all nodes", params...)

	health := etcdHealth{machines: len(machines)}

	// list of discovered etcd members, updated on each iteration
	members := map[string]struct{}{}
	membersListed := false

	var firstErr error

	for _, machine := range machines {
		// loop for each machine, the client created has endpoints which point to a single machine
		if err := func() error {
			ctx, cancel := context.WithTimeout(ctx, time.Second*5)
			defer cancel()

			c, err := r.talosconfigForMachines(ctx, tcp, machine)
			if err != nil {
				return err
			}

			defer c.Close() //nolint:errcheck

			svcs, err := c.ServiceInfo(ctx, etcdService)
			if err != nil {
				return err
			}

			health.inspected++

			// check that etcd service is healthy on the node
			if err := etcdServiceError(svcs); err != nil {
				return err
			}

			health.healthy++

			resp, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
			if err != nil {
				return err
			}

			compareMembers := membersListed
			membersListed = true

			for _, message := range resp.Messages {
				actualMembers := len(message.Members)
				expectedMembers := len(machines)

				votingMembers := 0

				for _, member := range message.Members {
					if !member.IsLearner {
						votingMembers++
					}
				}

				health.members = max(health.members, votingMembers)

				// a learner doesn't vote, so its health doesn't help etcd keep quorum
				if member := findEtcdMember(message.Members, &machine); member != nil && member.IsLearner {
					health.healthy--
				}

				node := message.Metadata.GetHostname()

				// check that the count of members is the same on all nodes
				if actualMembers != expectedMembers {
					return fmt.Errorf("%s: expected to have %d members, got %d", node, expectedMembers, actualMembers)
				}

				// check that member list is the same on all nodes
				for _, member := range message.Members {
					if _, found := members[member.Hostname]; compareMembers && !found {
						return fmt.Errorf("%s: found extra etcd member %s", node, member.Hostname)
					}

					members[member.Hostname] = struct{}{}
				}
			}

			return nil
		}(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("error checking etcd health on machine %q: %w", machine.Name, err)
		}
	}

	return health, firstErr
}

// etcdService is the Talos service running the etcd member of a control plane machine.
const etcdService = "etcd"

// etcdServiceError returns an error unless the etcd service is running and healthy on every node of the response.
func etcdServiceError(svcs []talosclient.ServiceInfo) error {
	for _, svc := range svcs {
		node := svc.Metadata.GetHostname()

		if len(svc.Service.GetEvents().GetEvents()) == 0 {
			return fmt.Errorf("%s: no events recorded yet for service %q", node, etcdService)
		}

		lastEvent := svc.Service.Events.Events[len(svc.Service.Events.Events)-1]
		if lastEvent.State != "Running" {
			return fmt.Errorf("%s: service %q not in expected state %q: current state [%s] %s", node, etcdService, "Running", lastEvent.State, lastEvent.Msg)
		}

		if !svc.Service.GetHealth().GetHealthy() {
			return fmt.Errorf("%s: service is not healthy: %s", node, etcdService)
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
// If the member could not be removed either way, the etcd-leaving mark is removed again and an error is returned:
// the caller must not delete the machine then, because its member would stay in the etcd cluster.
func (r *TalosControlPlaneReconciler) removeMachineFromEtcd(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) error {
	// Mark machine as leaving etcd so health check skips it even if reconciliation
	// crashes between leaving etcd and deleting the machine (prevents deadlock where
	// stopped etcd without DeletionTimestamp fails health checks forever).
	if err := r.setEtcdLeavingAnnotation(ctx, machine, true); err != nil {
		return err
	}

	leaveErr := r.etcdMemberLeave(ctx, tcp, machine, machines)
	if leaveErr == nil {
		return nil
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

	for _, other := range machines {
		if other.Name == machine.Name ||
			!other.DeletionTimestamp.IsZero() ||
			!other.Status.NodeRef.IsDefined() ||
			other.Annotations[etcdLeavingAnnotation] == "true" {
			continue
		}

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

// etcdMembersVia returns the etcd member list as seen by a control plane machine other than the given machine, that is
// not being deleted or leaving etcd. ok is false if there is no such machine.
func (r *TalosControlPlaneReconciler) etcdMembersVia(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) (members []*machineapi.EtcdMember, ok bool, err error) {
	var errs []error

	for _, other := range machines {
		if other.Name == machine.Name ||
			!other.DeletionTimestamp.IsZero() ||
			!other.Status.NodeRef.IsDefined() ||
			other.Annotations[etcdLeavingAnnotation] == "true" {
			continue
		}

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

	// For each etcd member, look through the list of machines and see if noderef matches
	for _, member := range memberList.Members {
		if member.Hostname == "" {
			return fmt.Errorf("discovered etcd member with empty hostname: %s", member)
		}

		present := false
		for i := range machines.Items {
			if etcdMemberMatchesMachine(member, &machines.Items[i]) {
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
