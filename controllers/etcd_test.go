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

func TestMachinesForEtcdHealthcheck(t *testing.T) {
	deleting := newMachine("deleting", func(m *clusterv1.Machine) {
		now := metav1.Now()
		m.DeletionTimestamp = &now
	})
	leaving := newMachine("leaving", func(m *clusterv1.Machine) {
		m.Annotations = map[string]string{etcdLeavingAnnotation: "true"}
	})
	remediating := newMachine("remediating", func(m *clusterv1.Machine) {
		conditions.MarkFalse(m, clusterv1.MachineOwnerRemediatedCondition,
			"WaitingForRemediation", clusterv1.ConditionSeverityWarning, "")
	})
	// A machine whose remediation is already done carries the condition set to True, which is a
	// distinct branch from the condition being absent: both must stay in the check set.
	remediated := newMachine("remediated", func(m *clusterv1.Machine) {
		conditions.MarkTrue(m, clusterv1.MachineOwnerRemediatedCondition)
	})
	healthy := newMachine("healthy", nil)

	// deleting, leaving, and remediating machines are all on their way out and must be excluded,
	// so a single unhealthy member cannot keep EtcdClusterHealthyCondition false forever. The
	// healthy and already-remediated machines must remain.
	got := machinesForEtcdHealthcheck([]clusterv1.Machine{deleting, leaving, remediating, remediated, healthy})

	gotNames := map[string]struct{}{}
	for _, m := range got {
		gotNames[m.Name] = struct{}{}
	}

	want := map[string]struct{}{"remediated": {}, "healthy": {}}
	if len(gotNames) != len(want) {
		t.Fatalf("expected check set %v, got %v", want, gotNames)
	}

	for name := range want {
		if _, ok := gotNames[name]; !ok {
			t.Fatalf("expected %q in the check set, got %v", name, gotNames)
		}
	}

	// When every owned machine is on its way out, the check set must be empty. etcdHealthcheck
	// relies on this to refuse a healthy verdict instead of running zero checks.
	if got := machinesForEtcdHealthcheck([]clusterv1.Machine{deleting, leaving, remediating}); len(got) != 0 {
		t.Fatalf("expected an empty check set when all machines are excluded, got %d", len(got))
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

	remediating := func(m *clusterv1.Machine) {
		conditions.MarkFalse(m, clusterv1.MachineOwnerRemediatedCondition, clusterv1.WaitingForRemediationReason, clusterv1.ConditionSeverityWarning, "")
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
	cpCRemediating := withNode("cp-c", "node-c", remediating)
	noNodeRef := newMachine("cp-new", nil)
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
			name:    "the orphan check is skipped while a machine lacks a noderef",
			owned:   []clusterv1.Machine{cpA, noNodeRef},
			members: members("node-a", "node-new"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			membership := newEtcdMembership(tt.owned, machinesForEtcdHealthcheck(tt.owned))

			err := membership.verify("node-a", tt.members)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("verify() = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("verify() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
