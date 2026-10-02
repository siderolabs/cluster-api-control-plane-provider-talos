// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"slices"
	"strings"
	"testing"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
)

func newMachine(name string, mutate func(*clusterv1.Machine)) clusterv1.Machine {
	m := clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if mutate != nil {
		mutate(&m)
	}

	return m
}

// markUnhealthy sets the two conditions the MachineHealthCheck controller sets when a machine fails a
// health check and its remediation is left to the owner.
func markUnhealthy(m *clusterv1.Machine) {
	conditions.MarkFalse(m, clusterv1.MachineHealthCheckSucceededCondition, clusterv1.NodeNotFoundReason, clusterv1.ConditionSeverityWarning, "")
	conditions.MarkFalse(m, clusterv1.MachineOwnerRemediatedCondition, clusterv1.WaitingForRemediationReason, clusterv1.ConditionSeverityWarning, "")
}

func TestRemainingMachines(t *testing.T) {
	deleting := newMachine("deleting", func(m *clusterv1.Machine) {
		now := metav1.Now()
		m.DeletionTimestamp = &now
	})
	leaving := newMachine("leaving", func(m *clusterv1.Machine) {
		m.Annotations = map[string]string{etcdLeavingAnnotation: "true"}
	})
	remediating := newMachine("remediating", markUnhealthy)
	// A machine whose remediation is already done carries the condition set to True, which is a
	// distinct branch from the condition being absent: both must stay in the set.
	remediated := newMachine("remediated", func(m *clusterv1.Machine) {
		conditions.MarkTrue(m, clusterv1.MachineOwnerRemediatedCondition)
	})
	// A machine that recovered on its own: the MachineHealthCheck controller sets
	// MachineHealthCheckSucceeded back to True but leaves MachineOwnerRemediated=False to the owner.
	// It is healthy again and must stay in the set, or it would be excluded forever.
	recovered := newMachine("recovered", func(m *clusterv1.Machine) {
		markUnhealthy(m)
		conditions.MarkTrue(m, clusterv1.MachineHealthCheckSucceededCondition)
	})
	healthy := newMachine("healthy", nil)

	// deleting, leaving, and remediating machines are all on their way out and must be excluded,
	// so a single unhealthy member cannot keep EtcdClusterHealthyCondition false forever. The
	// healthy, recovered, and already-remediated machines must remain.
	got := remainingMachines([]clusterv1.Machine{deleting, leaving, remediating, remediated, recovered, healthy})

	gotNames := map[string]struct{}{}
	for _, m := range got {
		gotNames[m.Name] = struct{}{}
	}

	want := map[string]struct{}{"remediated": {}, "recovered": {}, "healthy": {}}
	if len(gotNames) != len(want) {
		t.Fatalf("expected remaining machines %v, got %v", want, gotNames)
	}

	for name := range want {
		if _, ok := gotNames[name]; !ok {
			t.Fatalf("expected %q among the remaining machines, got %v", name, gotNames)
		}
	}

	// When every owned machine is on its way out, the set must be empty. etcdHealthcheck relies on
	// this to refuse a healthy verdict instead of running zero checks.
	if got := remainingMachines([]clusterv1.Machine{deleting, leaving, remediating}); len(got) != 0 {
		t.Fatalf("expected no remaining machines when all are excluded, got %d", len(got))
	}
}

func TestNodeNamesForMachine(t *testing.T) {
	withNodeRef := func(nodeName string, addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return newMachine("m", func(m *clusterv1.Machine) {
			m.Status.NodeRef = &corev1.ObjectReference{Name: nodeName}
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeNamesForMachine(tt.machine); !slices.Equal(got, tt.want) {
				t.Fatalf("nodeNamesForMachine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEtcdMembershipVerify(t *testing.T) {
	withNode := func(name, nodeName string, mutate func(*clusterv1.Machine), addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return newMachine(name, func(m *clusterv1.Machine) {
			m.Status.NodeRef = &corev1.ObjectReference{Name: nodeName}
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
	cpCRemediating := withNode("cp-c", "node-c", markUnhealthy)
	noNodeRef := newMachine("cp-new", nil)
	noNodeRefRemediating := newMachine("cp-stuck", markUnhealthy)
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
			name:    "a remediating machine whose member is still present is tolerated",
			owned:   []clusterv1.Machine{cpA, cpB, cpCRemediating},
			members: members("node-a", "node-b", "node-c"),
		},
		{
			name:    "a remediating machine whose member is already gone is tolerated",
			owned:   []clusterv1.Machine{cpA, cpB, cpCRemediating},
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
			owned:   []clusterv1.Machine{cpA, cpB, noNodeRefRemediating},
			members: members("node-a", "node-b", "node-x"),
			wantErr: `etcd member "node-x" does not match any control plane machine`,
		},
		{
			name:    "an excluded machine without a noderef is not required to have a member",
			owned:   []clusterv1.Machine{cpA, cpB, noNodeRefRemediating},
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
			// the old count check caught this; a member list that is shorter on a later node is
			// as much of a split view as a longer one
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
