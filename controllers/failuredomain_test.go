// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// machineInFailureDomain returns a control plane machine placed in the given failure domain.
func machineInFailureDomain(name, failureDomain string, deleting bool) clusterv1.Machine {
	machine := clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if failureDomain != "" {
		machine.Spec.FailureDomain = pointer.String(failureDomain)
	}

	if deleting {
		machine.ObjectMeta.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		machine.ObjectMeta.Finalizers = []string{clusterv1.MachineFinalizer}
	}

	return machine
}

func TestGetFailureDomain(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		failureDomains clusterv1.FailureDomains
		expected       []string
	}{
		{
			name:           "nil failure domains",
			failureDomains: nil,
			expected:       nil,
		},
		{
			name:           "empty failure domains",
			failureDomains: clusterv1.FailureDomains{},
			expected:       nil,
		},
		{
			name: "filters out non control plane domains",
			failureDomains: clusterv1.FailureDomains{
				"a": clusterv1.FailureDomainSpec{ControlPlane: true},
				"b": clusterv1.FailureDomainSpec{ControlPlane: false},
				"c": clusterv1.FailureDomainSpec{ControlPlane: true},
			},
			expected: []string{"a", "c"},
		},
		{
			name: "falls back to all domains when none are marked for control plane",
			failureDomains: clusterv1.FailureDomains{
				"c": clusterv1.FailureDomainSpec{ControlPlane: false},
				"a": clusterv1.FailureDomainSpec{ControlPlane: false},
				"b": clusterv1.FailureDomainSpec{ControlPlane: false},
			},
			expected: []string{"a", "b", "c"},
		},
		{
			name: "returns sorted list",
			failureDomains: clusterv1.FailureDomains{
				"c": clusterv1.FailureDomainSpec{ControlPlane: true},
				"a": clusterv1.FailureDomainSpec{ControlPlane: true},
				"b": clusterv1.FailureDomainSpec{ControlPlane: true},
			},
			expected: []string{"a", "b", "c"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := &clusterv1.Cluster{
				Status: clusterv1.ClusterStatus{
					FailureDomains: tt.failureDomains,
				},
			}

			r := &TalosControlPlaneReconciler{}

			assert.Equal(t, tt.expected, r.getFailureDomain(context.Background(), cluster))
		})
	}
}

func TestPickFailureDomain(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		failureDomains []string
		machines       []clusterv1.Machine
		expected       string
	}{
		{
			name:           "empty failure domains",
			failureDomains: nil,
			machines:       nil,
			expected:       "",
		},
		{
			name:           "single failure domain",
			failureDomains: []string{"a"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "a", false),
			},
			expected: "a",
		},
		{
			name:           "no machines picks first sorted domain",
			failureDomains: []string{"a", "b", "c"},
			machines:       nil,
			expected:       "a",
		},
		{
			name:           "picks least used domain",
			failureDomains: []string{"a", "b", "c"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "a", false),
				machineInFailureDomain("cp-2", "b", false),
				machineInFailureDomain("cp-3", "a", false),
				machineInFailureDomain("cp-4", "c", false),
				machineInFailureDomain("cp-5", "c", false),
			},
			expected: "b",
		},
		{
			name:           "deterministic tie break picks first sorted domain",
			failureDomains: []string{"a", "b", "c"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "a", false),
				machineInFailureDomain("cp-2", "b", false),
				machineInFailureDomain("cp-3", "c", false),
			},
			expected: "a",
		},
		{
			name:           "ignores machines being deleted",
			failureDomains: []string{"a", "b"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "a", false),
				machineInFailureDomain("cp-2", "b", true),
			},
			expected: "b",
		},
		{
			name:           "ignores machines without failure domain",
			failureDomains: []string{"a", "b"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "", false),
				machineInFailureDomain("cp-2", "a", false),
			},
			expected: "b",
		},
		{
			name:           "ignores machines in unknown failure domains",
			failureDomains: []string{"a", "b"},
			machines: []clusterv1.Machine{
				machineInFailureDomain("cp-1", "unknown", false),
				machineInFailureDomain("cp-2", "a", false),
			},
			expected: "b",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, pickFailureDomain(tt.failureDomains, tt.machines))
		})
	}
}
