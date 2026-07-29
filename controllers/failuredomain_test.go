// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const (
	currentVersion  = "v1.36.2"
	outdatedVersion = "v1.35.0"
)

// inFailureDomain places the machine in the given failure domain.
func inFailureDomain(failureDomain string) machineOption {
	return func(machine *clusterv1.Machine) {
		machine.Spec.FailureDomain = failureDomain
	}
}

// outdated marks the machine as running a version which no longer matches the control plane,
// which is what makes MachinesNeedingRollout pick it up.
func outdated() machineOption {
	return func(machine *clusterv1.Machine) {
		machine.Spec.Version = outdatedVersion
	}
}

// beingDeleted marks the machine as being deleted.
func beingDeleted() machineOption {
	return func(machine *clusterv1.Machine) {
		machine.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		machine.Finalizers = []string{clusterv1.MachineFinalizer}
	}
}

// createdAt sets the creation timestamp, which drives the Oldest() ordering used on scale down.
func createdAt(offset time.Duration) machineOption {
	return func(machine *clusterv1.Machine) {
		machine.CreationTimestamp = metav1.Time{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(offset)}
	}
}

// newControlPlaneMachine returns an up-to-date control plane machine with no failure domain set.
func newControlPlaneMachine(name string, opts ...machineOption) *clusterv1.Machine {
	machine := newMachine(name, func(machine *clusterv1.Machine) {
		machine.Spec.Version = currentVersion
	})

	for _, opt := range opts {
		opt(&machine)
	}

	return &machine
}

// newControlPlaneForTest builds a ControlPlane with the given failure domains and machines.
//
// infraObjects and talosConfigs are left empty on purpose: both matchers treat a missing entry as
// a match, so whether a machine needs a rollout is driven solely by its Kubernetes version.
func newControlPlaneForTest(failureDomains []clusterv1.FailureDomain, machines ...*clusterv1.Machine) *ControlPlane {
	return &ControlPlane{
		TCP: &controlplanev1.TalosControlPlane{
			Spec: controlplanev1.TalosControlPlaneSpec{
				Version: currentVersion,
			},
		},
		Cluster: &clusterv1.Cluster{
			Status: clusterv1.ClusterStatus{
				FailureDomains: failureDomains,
			},
		},
		Machines: collections.FromMachines(machines...),
	}
}

// controlPlaneDomains marks every given domain as suitable for the control plane.
func controlPlaneDomains(names ...string) []clusterv1.FailureDomain {
	failureDomains := make([]clusterv1.FailureDomain, 0, len(names))

	for _, name := range names {
		failureDomains = append(failureDomains, clusterv1.FailureDomain{Name: name, ControlPlane: ptr.To(true)})
	}

	return failureDomains
}

// spread counts the machines per failure domain.
func spread(machines collections.Machines) map[string]int {
	counts := map[string]int{}

	for _, machine := range machines {
		if machine.Spec.FailureDomain != "" {
			counts[machine.Spec.FailureDomain]++
		}
	}

	return counts
}

func TestControlPlaneFailureDomains(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		failureDomains []clusterv1.FailureDomain
		expected       []string
	}{
		{
			name:           "nil failure domains",
			failureDomains: nil,
			expected:       nil,
		},
		{
			name:           "empty failure domains",
			failureDomains: []clusterv1.FailureDomain{},
			expected:       nil,
		},
		{
			name: "filters out non control plane domains",
			failureDomains: []clusterv1.FailureDomain{
				{Name: "a", ControlPlane: ptr.To(true)},
				{Name: "b", ControlPlane: ptr.To(false)},
				{Name: "c", ControlPlane: ptr.To(true)},
			},
			expected: []string{"a", "c"},
		},
		{
			name: "falls back to all domains when none are marked for the control plane",
			failureDomains: []clusterv1.FailureDomain{
				{Name: "c"},
				{Name: "a", ControlPlane: ptr.To(false)},
				{Name: "b", ControlPlane: ptr.To(false)},
			},
			expected: []string{"a", "b", "c"},
		},
		{
			name: "keeps the single control plane domain even when others are available",
			failureDomains: []clusterv1.FailureDomain{
				{Name: "a", ControlPlane: ptr.To(true)},
				{Name: "b", ControlPlane: ptr.To(false)},
				{Name: "c", ControlPlane: ptr.To(false)},
			},
			expected: []string{"a"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var actual []string

			for _, failureDomain := range newControlPlaneForTest(tt.failureDomains).FailureDomains() {
				actual = append(actual, failureDomain.Name)
			}

			slices.Sort(actual)

			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestNextFailureDomainForScaleUp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		failureDomains []clusterv1.FailureDomain
		machines       []*clusterv1.Machine
		expected       []string
	}{
		{
			name:           "no failure domains",
			failureDomains: nil,
			expected:       nil,
		},
		{
			name:           "single failure domain",
			failureDomains: controlPlaneDomains("a"),
			machines:       []*clusterv1.Machine{newControlPlaneMachine("cp-1", inFailureDomain("a"))},
			expected:       []string{"a"},
		},
		{
			name:           "no machines picks any domain",
			failureDomains: controlPlaneDomains("a", "b", "c"),
			expected:       []string{"a", "b", "c"},
		},
		{
			name:           "picks the least used domain",
			failureDomains: controlPlaneDomains("a", "b", "c"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1", inFailureDomain("a")),
				newControlPlaneMachine("cp-2", inFailureDomain("b")),
				newControlPlaneMachine("cp-3", inFailureDomain("a")),
				newControlPlaneMachine("cp-4", inFailureDomain("c")),
				newControlPlaneMachine("cp-5", inFailureDomain("c")),
			},
			expected: []string{"b"},
		},
		{
			name:           "ignores machines being deleted",
			failureDomains: controlPlaneDomains("a", "b"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1", inFailureDomain("a")),
				newControlPlaneMachine("cp-2", inFailureDomain("b"), beingDeleted()),
			},
			expected: []string{"b"},
		},
		{
			name:           "ignores machines without a failure domain",
			failureDomains: controlPlaneDomains("a", "b"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1"),
				newControlPlaneMachine("cp-2", inFailureDomain("a")),
			},
			expected: []string{"b"},
		},
		{
			name:           "ignores machines in unknown failure domains",
			failureDomains: controlPlaneDomains("a", "b"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1", inFailureDomain("unknown")),
				newControlPlaneMachine("cp-2", inFailureDomain("a")),
			},
			expected: []string{"b"},
		},
		{
			// outdated machines are about to be replaced, so spreading the up-to-date ones wins.
			name:           "prefers spreading up-to-date machines over outdated ones",
			failureDomains: controlPlaneDomains("a", "b", "c"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1", inFailureDomain("a"), outdated()),
				newControlPlaneMachine("cp-2", inFailureDomain("b"), outdated()),
				newControlPlaneMachine("cp-3", inFailureDomain("c")),
			},
			expected: []string{"a", "b"},
		},
		{
			// tie on up-to-date machines, so the domain with fewer machines overall wins.
			name:           "breaks ties on up-to-date machines by the overall machine count",
			failureDomains: controlPlaneDomains("a", "b"),
			machines: []*clusterv1.Machine{
				newControlPlaneMachine("cp-1", inFailureDomain("a"), outdated()),
				newControlPlaneMachine("cp-2", inFailureDomain("a"), outdated()),
				newControlPlaneMachine("cp-3", inFailureDomain("b"), outdated()),
			},
			expected: []string{"b"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := newControlPlaneForTest(tt.failureDomains, tt.machines...).NextFailureDomainForScaleUp(context.Background())

			if tt.expected == nil {
				assert.Empty(t, actual)

				return
			}

			// ties are broken at random, so any of the expected domains is a valid answer.
			assert.Contains(t, tt.expected, actual)
		})
	}
}

// TestScaleUpSpreadsAcrossFailureDomains walks the initial scale up of a control plane and asserts
// that the machines end up evenly spread, one per failure domain.
func TestScaleUpSpreadsAcrossFailureDomains(t *testing.T) {
	t.Parallel()

	const replicas = 3

	ctx := context.Background()
	failureDomains := controlPlaneDomains("a", "b", "c")

	machines := make([]*clusterv1.Machine, 0, replicas)

	for i := range replicas {
		failureDomain := newControlPlaneForTest(failureDomains, machines...).NextFailureDomainForScaleUp(ctx)
		require.NotEmpty(t, failureDomain)

		machines = append(machines, newControlPlaneMachine(fmt.Sprintf("cp-%d", i+1), inFailureDomain(failureDomain), createdAt(time.Duration(i)*time.Minute)))
	}

	assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1}, spread(collections.FromMachines(machines...)))
}
