// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/collections"
)

func TestSelectMachineForScaleDown(t *testing.T) {
	createdAt := func(age time.Duration) func(*clusterv1.Machine) {
		return func(m *clusterv1.Machine) {
			m.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
		}
	}
	chain := func(fns ...func(*clusterv1.Machine)) func(*clusterv1.Machine) {
		return func(m *clusterv1.Machine) {
			for _, fn := range fns {
				fn(m)
			}
		}
	}
	annotatedForDeletion := func(m *clusterv1.Machine) {
		m.Annotations = map[string]string{clusterv1.DeleteMachineAnnotation: ""}
	}

	oldest := newMachine("oldest", createdAt(3*time.Hour))
	middle := newMachine("middle", createdAt(2*time.Hour))
	newest := newMachine("newest", createdAt(time.Hour))
	unhealthyNewest := newMachine("unhealthy", chain(createdAt(time.Hour), markUnhealthy))
	annotatedMiddle := newMachine("annotated", chain(createdAt(2*time.Hour), annotatedForDeletion))

	tests := []struct {
		name     string
		machines []clusterv1.Machine
		outdated []clusterv1.Machine
		want     string
	}{
		{
			name:     "the oldest machine by default",
			machines: []clusterv1.Machine{oldest, middle, newest},
			want:     "oldest",
		},
		{
			name:     "an outdated machine over a newer one",
			machines: []clusterv1.Machine{oldest, middle, newest},
			outdated: []clusterv1.Machine{middle},
			want:     "middle",
		},
		{
			// removing a healthy member while the unhealthy one is down could cost etcd its quorum
			name:     "a machine flagged for remediation over the oldest",
			machines: []clusterv1.Machine{oldest, middle, unhealthyNewest},
			want:     "unhealthy",
		},
		{
			name:     "a machine flagged for remediation over an outdated one",
			machines: []clusterv1.Machine{oldest, middle, unhealthyNewest},
			outdated: []clusterv1.Machine{oldest, middle},
			want:     "unhealthy",
		},
		{
			name:     "a machine annotated for deletion over one flagged for remediation",
			machines: []clusterv1.Machine{oldest, annotatedMiddle, unhealthyNewest},
			want:     "annotated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controlPlane := &ControlPlane{Machines: collections.FromMachines(ptrs(tt.machines)...)}

			got, err := selectMachineForScaleDown(controlPlane, collections.FromMachines(ptrs(tt.outdated)...))
			if err != nil {
				t.Fatalf("selectMachineForScaleDown() error = %v", err)
			}

			if got.Name != tt.want {
				t.Fatalf("selectMachineForScaleDown() = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

func ptrs(machines []clusterv1.Machine) []*clusterv1.Machine {
	out := make([]*clusterv1.Machine, 0, len(machines))

	for i := range machines {
		out = append(out, &machines[i])
	}

	return out
}
