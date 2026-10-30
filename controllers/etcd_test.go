// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

func TestEtcdHealthConditionReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		health etcdHealth
		reason string
	}{
		{
			name:   "nothing inspected",
			health: etcdHealth{machines: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "one unhealthy member out of 3",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 2, members: 3},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "one unreachable machine out of 3",
			health: etcdHealth{machines: 3, inspected: 2, healthy: 2, members: 3},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "two unhealthy members out of 3",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
		{
			// The unreachable machines may well run healthy members.
			name:   "two unreachable machines out of 3",
			health: etcdHealth{machines: 3, inspected: 1, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "one unhealthy and one unreachable machine out of 3",
			health: etcdHealth{machines: 3, inspected: 2, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "two unhealthy members and one unreachable machine out of 5",
			health: etcdHealth{machines: 5, inspected: 4, healthy: 2, members: 5},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "three unhealthy members out of 5",
			health: etcdHealth{machines: 5, inspected: 5, healthy: 2, members: 5},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
		{
			// A learner that is not healthy yet doesn't count towards quorum: 1 voting member, which is healthy.
			name:   "unhealthy learner joining a single member",
			health: etcdHealth{machines: 2, inspected: 2, healthy: 1, members: 1},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "no member list",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 1},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
		{
			// The member of the machine being deleted is not inspected, and may still run: with it, etcd has quorum.
			name:   "one healthy and one unhealthy member, and a member of a machine being deleted",
			health: etcdHealth{machines: 2, inspected: 2, healthy: 1, members: 3, excluded: 1},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			g.Expect(tc.health.conditionReason()).To(Equal(tc.reason))
		})
	}
}

func newMachine(name string, mutate func(*clusterv1.Machine)) clusterv1.Machine {
	m := clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if mutate != nil {
		mutate(&m)
	}

	return m
}

func markDeleting(m *clusterv1.Machine) {
	now := metav1.Now()
	m.DeletionTimestamp = &now
}

func markLeaving(m *clusterv1.Machine) {
	m.Annotations = map[string]string{etcdLeavingAnnotation: "true"}
}

func TestRemainingMachines(t *testing.T) {
	deleting := newMachine("deleting", markDeleting)
	leaving := newMachine("leaving", markLeaving)
	notLeaving := newMachine("not-leaving", func(m *clusterv1.Machine) {
		m.Annotations = map[string]string{etcdLeavingAnnotation: "false"}
	})
	healthy := newMachine("healthy", nil)

	got := remainingMachines([]clusterv1.Machine{deleting, leaving, notLeaving, healthy})

	gotNames := make([]string, 0, len(got))
	for _, m := range got {
		gotNames = append(gotNames, m.Name)
	}

	if want := []string{"not-leaving", "healthy"}; !slices.Equal(gotNames, want) {
		t.Fatalf("remainingMachines() = %q, want %q", gotNames, want)
	}

	if got := remainingMachines([]clusterv1.Machine{deleting, leaving}); len(got) != 0 {
		t.Fatalf("expected no remaining machines when all are excluded, got %d", len(got))
	}
}

func TestNodeNamesForMachine(t *testing.T) {
	withNodeRef := func(nodeName string, addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return newMachine("m", func(m *clusterv1.Machine) {
			m.Status.NodeRef = clusterv1.MachineNodeReference{Name: nodeName}
			m.Status.Addresses = addresses
		})
	}

	tests := []struct {
		name    string
		machine clusterv1.Machine
		want    []string
	}{
		{
			name:    "noderef name",
			machine: withNodeRef("node-a"),
			want:    []string{"node-a"},
		},
		{
			name:    "fqdn noderef is trimmed to the first label and lowercased",
			machine: withNodeRef("Node-A.example.com"),
			want:    []string{"node-a"},
		},
		{
			name: "hostname address is a candidate next to the noderef name",
			machine: withNodeRef("node-a",
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-b"}),
			want: []string{"node-a", "host-b"},
		},
		{
			name: "every hostname address is a candidate, other address types are ignored",
			machine: withNodeRef("node-a",
				clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: "10.0.0.1"},
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-b.example.com"},
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-c"}),
			want: []string{"node-a", "host-b", "host-c"},
		},
		{
			name: "without a noderef only the hostname addresses are candidates",
			machine: withNodeRef("",
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-b"}),
			want: []string{"host-b"},
		},
		{
			name:    "without a noderef or hostname address there are no candidates",
			machine: withNodeRef(""),
			want:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeNamesForMachine(&tt.machine); !slices.Equal(got, tt.want) {
				t.Fatalf("nodeNamesForMachine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEtcdMembershipVerify(t *testing.T) {
	withNode := func(name, nodeName string, mutate func(*clusterv1.Machine), addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return newMachine(name, func(m *clusterv1.Machine) {
			m.Status.NodeRef = clusterv1.MachineNodeReference{Name: nodeName}
			m.Status.Addresses = addresses

			if mutate != nil {
				mutate(m)
			}
		})
	}

	members := func(hostnames ...string) []*machineapi.EtcdMember {
		out := make([]*machineapi.EtcdMember, 0, len(hostnames))

		for i, hostname := range hostnames {
			out = append(out, &machineapi.EtcdMember{Id: uint64(i), Hostname: hostname})
		}

		return out
	}

	cpA := withNode("cp-a", "node-a", nil)
	cpB := withNode("cp-b", "node-b", nil)
	cpC := withNode("cp-c", "node-c", nil)
	cpCLeaving := withNode("cp-c", "node-c", markLeaving)
	noNodeRef := newMachine("cp-new", nil)
	noNodeRefDeleting := newMachine("cp-stuck", markDeleting)
	// the infrastructure machine name is reported as MachineHostName and differs from the node hostname
	cpInfraName := withNode("cp-d", "node-d", nil,
		clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "infra-cp-d"})

	tests := []struct {
		name    string
		owned   []clusterv1.Machine
		members []*machineapi.EtcdMember
		wantErr string
	}{
		{
			name:    "every machine has a member",
			owned:   []clusterv1.Machine{cpA, cpB, cpC},
			members: members("node-a", "NODE-B", "node-c"),
		},
		{
			name:    "a machine without a member is reported",
			owned:   []clusterv1.Machine{cpA, cpB, cpC},
			members: members("node-a", "node-b"),
			wantErr: `etcd is missing a member for control plane machine "cp-c"`,
		},
		{
			name:    "a member matching no machine is an orphan",
			owned:   []clusterv1.Machine{cpA, cpB},
			members: members("node-a", "node-b", "node-x"),
			wantErr: `etcd member "node-x" does not match any control plane machine`,
		},
		{
			name:    "a leaving machine whose member is still present is tolerated",
			owned:   []clusterv1.Machine{cpA, cpB, cpCLeaving},
			members: members("node-a", "node-b", "node-c"),
		},
		{
			name:    "a leaving machine whose member is already gone is tolerated",
			owned:   []clusterv1.Machine{cpA, cpB, cpCLeaving},
			members: members("node-a", "node-b"),
		},
		{
			name:    "a member matches the node hostname even when MachineHostName differs",
			owned:   []clusterv1.Machine{cpA, cpInfraName},
			members: members("node-a", "node-d"),
		},
		{
			name:    "a member matches the MachineHostName address",
			owned:   []clusterv1.Machine{cpA, cpInfraName},
			members: members("node-a", "infra-cp-d"),
		},
		{
			name:    "a machine without a noderef yet claims one unmatched member",
			owned:   []clusterv1.Machine{cpA, noNodeRef},
			members: members("node-a", "node-new"),
		},
		{
			name:    "a machine without a noderef yet must still have a member",
			owned:   []clusterv1.Machine{cpA, noNodeRef},
			members: members("node-a"),
			wantErr: `etcd is missing a member for 1 of the control plane machines without a noderef yet ["cp-new"]`,
		},
		{
			name:    "a machine without a noderef yet does not excuse a second unmatched member",
			owned:   []clusterv1.Machine{cpA, noNodeRef},
			members: members("node-a", "node-new", "node-x"),
			wantErr: `etcd member "node-x" does not match any control plane machine`,
		},
		{
			name:    "an excluded machine without a noderef does not disable the orphan check",
			owned:   []clusterv1.Machine{cpA, cpB, noNodeRefDeleting},
			members: members("node-a", "node-b", "node-x"),
			wantErr: `etcd member "node-x" does not match any control plane machine`,
		},
		{
			name:    "an excluded machine without a noderef is not required to have a member",
			owned:   []clusterv1.Machine{cpA, cpB, noNodeRefDeleting},
			members: members("node-a", "node-b"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			membership := newEtcdMembership(tt.owned, remainingMachines(tt.owned))

			got, err := membership.verify("node-a", tt.members)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("verify() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("verify() = %v, want error containing %q", err, tt.wantErr)
			case tt.wantErr == "" && len(got) != len(tt.members):
				t.Fatalf("verify() returned %d members, want %d", len(got), len(tt.members))
			}

			if err == nil {
				for _, member := range tt.members {
					if _, ok := got[strings.ToLower(member.Hostname)]; !ok {
						t.Fatalf("verify() returned members %v without %q", got, member.Hostname)
					}
				}
			}
		})
	}
}

func TestCompareEtcdMembers(t *testing.T) {
	set := func(names ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(names))

		for _, name := range names {
			out[name] = struct{}{}
		}

		return out
	}

	tests := []struct {
		name     string
		members  map[string]struct{}
		previous map[string]struct{}
		wantErr  string
	}{
		{
			name:     "same members",
			members:  set("node-a", "node-b"),
			previous: set("node-b", "node-a"),
		},
		{
			name:     "a member only this node knows",
			members:  set("node-a", "node-b", "node-x"),
			previous: set("node-a", "node-b"),
			wantErr:  `node-b: etcd member "node-x" is not known to node-a`,
		},
		{
			// a member list that is shorter on a later node is as much of a split view as a longer one
			name:     "a member only the previous node knows",
			members:  set("node-a", "node-b"),
			previous: set("node-a", "node-b", "node-x"),
			wantErr:  `node-b: etcd member "node-x" known to node-a is missing`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := compareEtcdMembers("node-b", tt.members, "node-a", tt.previous)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("compareEtcdMembers() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("compareEtcdMembers() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestEtcdServiceErrorWithoutService(t *testing.T) {
	g := NewWithT(t)

	g.Expect(etcdServiceError(nil)).To(MatchError(ContainSubstring(`service "etcd" not found`)))
}

func TestIdentifyEtcdMember(t *testing.T) {
	now := time.Now()

	named := newTestMachine("m1", now)
	// A machine without a nodeRef nor a MachineHostName address has no host names.
	unnamed := newTestMachine("new", now, withoutNodeRef())
	other := newTestMachine("m2", now)
	otherUnnamed := newTestMachine("new2", now, withoutNodeRef())

	for _, tc := range []struct {
		name       string
		machine    *clusterv1.Machine
		machines   []*clusterv1.Machine
		members    []*machineapi.EtcdMember
		wantMember string
		wantKnown  bool
	}{
		{
			name:       "matched by name",
			machine:    named,
			machines:   []*clusterv1.Machine{named, other},
			members:    etcdMembersFor("m1", "m2"),
			wantMember: "m1",
			wantKnown:  true,
		},
		{
			name:      "not a member",
			machine:   named,
			machines:  []*clusterv1.Machine{named, other},
			members:   etcdMembersFor("m2", "x"),
			wantKnown: true,
		},
		{
			name:      "without host names, every member matches another machine",
			machine:   unnamed,
			machines:  []*clusterv1.Machine{unnamed, other},
			members:   etcdMembersFor("m2"),
			wantKnown: true,
		},
		{
			name:       "without host names, by elimination",
			machine:    unnamed,
			machines:   []*clusterv1.Machine{unnamed, other},
			members:    etcdMembersFor("m2", "ip-10-0-0-1"),
			wantMember: "ip-10-0-0-1",
			wantKnown:  true,
		},
		{
			name:     "without host names, several unmatched members",
			machine:  unnamed,
			machines: []*clusterv1.Machine{unnamed, other},
			members:  etcdMembersFor("m2", "ip-10-0-0-1", "ip-10-0-0-2"),
		},
		{
			name:     "without host names, another machine without host names",
			machine:  unnamed,
			machines: []*clusterv1.Machine{unnamed, otherUnnamed, other},
			members:  etcdMembersFor("m2", "ip-10-0-0-1"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			member, known := identifyEtcdMember(tc.members, tc.machine, tc.machines)
			g.Expect(known).To(Equal(tc.wantKnown))

			if tc.wantMember == "" {
				g.Expect(member).To(BeNil())
			} else {
				g.Expect(member).NotTo(BeNil())
				g.Expect(member.Hostname).To(Equal(tc.wantMember))
			}
		})
	}
}
