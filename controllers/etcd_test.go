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

// withMemberID records the etcd member ID of a machine, as the etcd health check does.
func withMemberID(id uint64) func(*clusterv1.Machine) {
	return func(m *clusterv1.Machine) {
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}

		m.Annotations[etcdMemberIDAnnotation] = formatEtcdMemberID(id)
	}
}

// member returns an etcd member.
func member(id uint64, hostname string) *machineapi.EtcdMember {
	return &machineapi.EtcdMember{Id: id, Hostname: hostname}
}

func TestEtcdMemberIDs(t *testing.T) {
	g := NewWithT(t)

	recorded := newMachine("recorded", withMemberID(0x3602b0aed27e8f41))
	overridden := newMachine("overridden", withMemberID(1))
	invalid := newMachine("invalid", func(m *clusterv1.Machine) {
		m.Annotations = map[string]string{etcdMemberIDAnnotation: "not-an-id"}
	})
	none := newMachine("none", nil)

	g.Expect(recorded.Annotations[etcdMemberIDAnnotation]).To(Equal("3602b0aed27e8f41"))
	g.Expect(etcdMemberIDs([]*clusterv1.Machine{&recorded, &overridden, &invalid, &none}, map[string]uint64{"overridden": 2})).To(Equal(map[string]uint64{
		"recorded":   0x3602b0aed27e8f41,
		"overridden": 2,
	}))
}

func TestMatchEtcdMembers(t *testing.T) {
	withNode := func(name string, mutate ...func(*clusterv1.Machine)) *clusterv1.Machine {
		m := newMachine(name, func(m *clusterv1.Machine) {
			m.Status.NodeRef = clusterv1.MachineNodeReference{Name: name}

			for _, f := range mutate {
				f(m)
			}
		})

		return &m
	}

	for _, tt := range []struct {
		name       string
		machines   []*clusterv1.Machine
		members    []*machineapi.EtcdMember
		want       map[string]uint64
		unmatched  []uint64
		unresolved []string
	}{
		{
			name:     "by member ID, whatever the member name",
			machines: []*clusterv1.Machine{withNode("cp-a", withMemberID(1)), withNode("cp-b", withMemberID(2))},
			members:  []*machineapi.EtcdMember{member(1, "cp-a"), member(2, "talos-4y4-bo8")},
			want:     map[string]uint64{"cp-a": 1, "cp-b": 2},
		},
		{
			name:     "by host name without a member ID",
			machines: []*clusterv1.Machine{withNode("cp-a"), withNode("cp-b")},
			members:  []*machineapi.EtcdMember{member(1, "CP-A"), member(2, "cp-b")},
			want:     map[string]uint64{"cp-a": 1, "cp-b": 2},
		},
		{
			// the member of a machine with a known ID that is not in the list is gone, whatever the member names
			name:      "a known member ID that is not a member is not matched by host name",
			machines:  []*clusterv1.Machine{withNode("cp-a", withMemberID(1)), withNode("cp-b", withMemberID(9))},
			members:   []*machineapi.EtcdMember{member(1, "cp-a"), member(2, "cp-b")},
			want:      map[string]uint64{"cp-a": 1},
			unmatched: []uint64{2},
		},
		{
			// an orphaned member carries the host name of the new machine, which runs another member
			name:      "a member that a machine runs by ID is not matched by host name",
			machines:  []*clusterv1.Machine{withNode("cp-a", withMemberID(3)), withNode("cp-b")},
			members:   []*machineapi.EtcdMember{member(1, "cp-a"), member(2, "cp-b"), member(3, "talos-4y4-bo8")},
			want:      map[string]uint64{"cp-a": 3, "cp-b": 2},
			unmatched: []uint64{1},
		},
		{
			name:       "a machine with neither a member ID nor a matching host name is unresolved",
			machines:   []*clusterv1.Machine{withNode("cp-a", withMemberID(1)), withNode("cp-b"), newMachinePtr("cp-new")},
			members:    []*machineapi.EtcdMember{member(1, "cp-a"), member(2, "talos-4y4-bo8")},
			want:       map[string]uint64{"cp-a": 1},
			unmatched:  []uint64{2},
			unresolved: []string{"cp-b", "cp-new"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			match := matchEtcdMembers(tt.members, tt.machines, etcdMemberIDs(tt.machines, nil))

			got := map[string]uint64{}
			for name, member := range match.members {
				got[name] = member.Id
			}

			unmatched := []uint64{}
			for _, member := range match.unmatched {
				unmatched = append(unmatched, member.Id)
			}

			g.Expect(got).To(Equal(tt.want))
			g.Expect(unmatched).To(ConsistOf(tt.unmatched))
			g.Expect(match.unresolved).To(Equal(tt.unresolved))
		})
	}
}

func newMachinePtr(name string, mutate ...func(*clusterv1.Machine)) *clusterv1.Machine {
	m := newMachine(name, func(m *clusterv1.Machine) {
		for _, f := range mutate {
			f(m)
		}
	})

	return &m
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
			out = append(out, member(uint64(i+1), hostname))
		}

		return out
	}

	with := func(base []*machineapi.EtcdMember, extra ...*machineapi.EtcdMember) []*machineapi.EtcdMember {
		return append(base, extra...)
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
	// cp-e joined etcd under a temporary hostname, and its member ID is recorded
	cpTempName := withNode("cp-e", "cp-e", withMemberID(100))

	tests := []struct {
		name     string
		owned    []clusterv1.Machine
		members  []*machineapi.EtcdMember
		observed map[string]uint64
		wantErr  string
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
			wantErr: `etcd is missing a member for 1 of the control plane machines ["cp-c"]`,
		},
		{
			name:    "a member matching no machine is an orphan",
			owned:   []clusterv1.Machine{cpA, cpB},
			members: members("node-a", "node-b", "node-x"),
			wantErr: `etcd member "node-x" does not match any control plane machine (member ID 0000000000000003)`,
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
			name:    "a member that joined under a temporary hostname matches by its recorded member ID",
			owned:   []clusterv1.Machine{cpA, cpTempName},
			members: with(members("node-a"), member(100, "talos-4y4-bo8")),
		},
		{
			name:     "a member that joined under a temporary hostname matches by its observed member ID",
			owned:    []clusterv1.Machine{cpA, cpB},
			members:  with(members("node-a"), member(100, "talos-4y4-bo8")),
			observed: map[string]uint64{"cp-b": 100},
		},
		{
			name:     "an observed member ID takes precedence over the recorded one",
			owned:    []clusterv1.Machine{cpA, cpTempName},
			members:  with(members("node-a"), member(100, "talos-4y4-bo8")),
			observed: map[string]uint64{"cp-e": 101},
			wantErr:  `etcd is missing member 0000000000000065 of control plane machine "cp-e"`,
		},
		{
			// the member named cp-e belongs to a deleted machine whose replacement got the same name
			name:    "a member with the host name of a machine that runs another member is an orphan",
			owned:   []clusterv1.Machine{cpA, cpTempName},
			members: with(members("node-a", "cp-e"), member(100, "talos-4y4-bo8")),
			wantErr: `etcd member "cp-e" does not match any control plane machine`,
		},
		{
			name:    "a machine whose member is not identified claims one unmatched member",
			owned:   []clusterv1.Machine{cpA, cpB},
			members: with(members("node-a"), member(100, "talos-4y4-bo8")),
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
			wantErr: `etcd is missing a member for 1 of the control plane machines ["cp-new"]`,
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
			membership := newEtcdMembership(tt.owned, remainingMachines(tt.owned), tt.observed)

			err := membership.verify("node-a", membership.match(tt.members))

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("verify() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("verify() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompareEtcdMembers(t *testing.T) {
	set := func(members ...*machineapi.EtcdMember) map[uint64]string {
		return etcdMemberSet(members)
	}

	tests := []struct {
		name     string
		members  map[uint64]string
		previous map[uint64]string
		wantErr  string
	}{
		{
			name:     "same members",
			members:  set(member(1, "node-a"), member(2, "node-b")),
			previous: set(member(2, "node-b"), member(1, "node-a")),
		},
		{
			name:     "a member only this node knows",
			members:  set(member(1, "node-a"), member(2, "node-b"), member(3, "node-x")),
			previous: set(member(1, "node-a"), member(2, "node-b")),
			wantErr:  `node-b: etcd member "node-x" (0000000000000003) is not known to node-a`,
		},
		{
			// a member list that is shorter on a later node is as much of a split view as a longer one
			name:     "a member only the previous node knows",
			members:  set(member(1, "node-a"), member(2, "node-b")),
			previous: set(member(1, "node-a"), member(2, "node-b"), member(3, "node-x")),
			wantErr:  `node-b: etcd member "node-x" (0000000000000003) known to node-a is missing`,
		},
		{
			// the same name does not make it the same member
			name:     "a member with another ID",
			members:  set(member(1, "node-a"), member(4, "node-b")),
			previous: set(member(1, "node-a"), member(2, "node-b")),
			wantErr:  `node-b: etcd member "node-b" (0000000000000004) is not known to node-a`,
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
	withID := newTestMachine("m3", now, withAnnotation(etcdMemberIDAnnotation, formatEtcdMemberID(100)))
	otherWithID := newTestMachine("m4", now, withAnnotation(etcdMemberIDAnnotation, formatEtcdMemberID(101)))
	tempHostname := member(100, "talos-4y4-bo8")

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
			name:       "matched by member ID",
			machine:    withID,
			machines:   []*clusterv1.Machine{withID, other},
			members:    append(etcdMembersFor("m2", "m3"), tempHostname),
			wantMember: "talos-4y4-bo8",
			wantKnown:  true,
		},
		{
			name:      "not a member by member ID, whatever the member names",
			machine:   withID,
			machines:  []*clusterv1.Machine{withID, other},
			members:   etcdMembersFor("m2", "m3"),
			wantKnown: true,
		},
		{
			name:      "not a member",
			machine:   named,
			machines:  []*clusterv1.Machine{named, other},
			members:   etcdMembersFor("m2"),
			wantKnown: true,
		},
		{
			// m1 may have joined etcd under a temporary hostname
			name:       "by elimination when the name doesn't match",
			machine:    named,
			machines:   []*clusterv1.Machine{named, other},
			members:    etcdMembersFor("m2", "x"),
			wantMember: "x",
			wantKnown:  true,
		},
		{
			name:       "by elimination, other machines matched by member ID",
			machine:    unnamed,
			machines:   []*clusterv1.Machine{unnamed, otherWithID},
			members:    []*machineapi.EtcdMember{member(101, "talos-4y4-bo8"), member(5, "ip-10-0-0-1")},
			wantMember: "ip-10-0-0-1",
			wantKnown:  true,
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
