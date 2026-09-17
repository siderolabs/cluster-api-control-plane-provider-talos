// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"

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

func TestNodeNameForMachine(t *testing.T) {
	withNodeRef := func(nodeName string, addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return newMachine("m", func(m *clusterv1.Machine) {
			m.Status.NodeRef = &corev1.ObjectReference{Name: nodeName}
			m.Status.Addresses = addresses
		})
	}

	tests := []struct {
		name    string
		machine clusterv1.Machine
		want    string
	}{
		{
			name:    "noderef name",
			machine: withNodeRef("node-a"),
			want:    "node-a",
		},
		{
			name:    "fqdn noderef is trimmed to the first label",
			machine: withNodeRef("node-a.example.com"),
			want:    "node-a",
		},
		{
			name: "hostname address overrides the noderef name",
			machine: withNodeRef("node-a",
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-b"}),
			want: "host-b",
		},
		{
			name: "hostname address is also trimmed to the first label",
			machine: withNodeRef("node-a",
				clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: "10.0.0.1"},
				clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "host-b.example.com"}),
			want: "host-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeNameForMachine(tt.machine); got != tt.want {
				t.Fatalf("nodeNameForMachine() = %q, want %q", got, tt.want)
			}
		})
	}
}
