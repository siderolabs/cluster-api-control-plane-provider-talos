// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
	"sigs.k8s.io/cluster-api/util/collections"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// fakeCaller stands in for the runtime extension.
type fakeCaller struct {
	extensions []string

	// respond fills in the response; nil means "claim the whole Machine spec diff".
	respond func(req *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse)

	calls int
}

func (f *fakeCaller) GetAllExtensions(context.Context, runtimecatalog.Hook, metav1.Object) ([]string, error) {
	return f.extensions, nil
}

func (f *fakeCaller) CallExtension(_ context.Context, _ runtimecatalog.Hook, _ metav1.Object, _ string, request, response any) error {
	f.calls++

	req, ok := request.(*runtimehooksv1.CanUpdateMachineRequest)
	if !ok {
		return nil
	}

	resp, ok := response.(*runtimehooksv1.CanUpdateMachineResponse)
	if !ok {
		return nil
	}

	resp.Status = runtimehooksv1.ResponseStatusSuccess

	if f.respond != nil {
		f.respond(req, resp)

		return nil
	}

	// Claim the version change, which is the whole diff these tests set up.
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"version": req.Desired.Machine.Spec.Version}})
	if err != nil {
		return err
	}

	resp.MachinePatch = runtimehooksv1.Patch{PatchType: runtimehooksv1.JSONMergePatchType, Patch: patch}

	return nil
}

type inPlaceFixture struct {
	reconciler   *TalosControlPlaneReconciler
	tcp          *controlplanev1.TalosControlPlane
	controlPlane *ControlPlane
	machine      *clusterv1.Machine
	caller       *fakeCaller
}

// newInPlaceFixture builds a single-machine control plane that is outdated only by its
// Kubernetes version, which the extension can absorb.
func newInPlaceFixture(t *testing.T, machineAnnotations map[string]string) *inPlaceFixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clusterv1.AddToScheme(scheme))
	require.NoError(t, runtimev1.AddToScheme(scheme))
	require.NoError(t, cabptv1.AddToScheme(scheme))
	require.NoError(t, controlplanev1.AddToScheme(scheme))

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "tcp", Namespace: "default"},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.34.0",
			ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
				ControlPlaneConfig: cabptv1.TalosConfigSpec{GenerateType: "controlplane"},
			},
		},
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "machine-1",
			Namespace:   "default",
			Annotations: machineAnnotations,
		},
		Spec: clusterv1.MachineSpec{ClusterName: "test", Version: "v1.33.0"},
		Status: clusterv1.MachineStatus{
			NodeRef: clusterv1.MachineNodeReference{Name: "node-1"},
		},
	}

	talosConfig := &cabptv1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-1", Namespace: "default"},
		Spec:       cabptv1.TalosConfigSpec{GenerateType: "controlplane"},
	}

	infra := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
		"kind":       "TinkerbellMachine",
		"metadata":   map[string]any{"name": "machine-1", "namespace": "default"},
		"spec":       map[string]any{"hardwareName": "hw-1"},
	}}

	caller := &fakeCaller{extensions: []string{"can-update-machine"}}

	return &inPlaceFixture{
		reconciler: &TalosControlPlaneReconciler{
			Client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine, talosConfig).Build(),
			Log:           ctrl.Log.WithName("test"),
			Scheme:        scheme,
			RuntimeClient: caller,
		},
		tcp: tcp,
		controlPlane: &ControlPlane{
			TCP:          tcp,
			Machines:     collections.FromMachines(machine),
			infraObjects: map[string]*unstructured.Unstructured{"machine-1": infra},
			talosConfigs: map[string]*cabptv1.TalosConfig{"machine-1": talosConfig},
		},
		machine: machine,
		caller:  caller,
	}
}

// The gate has to hold even with the extension registered: without the feature gate the
// provider must behave exactly as it did before.
func TestReconcileInPlaceUpdates_DisabledWithoutFeatureGate(t *testing.T) {
	f := newInPlaceFixture(t, nil)

	claimed, err := f.reconciler.reconcileInPlaceUpdates(
		context.Background(), f.tcp, f.controlPlane, collections.FromMachines(f.machine))

	require.NoError(t, err)
	assert.Empty(t, claimed)
	assert.Zero(t, f.caller.calls, "the extension must not be consulted while the gate is off")
}

// A machine already carrying the annotation stays claimed so the rollout path leaves it
// alone, and no second machine is started concurrently.
func TestReconcileInPlaceUpdates_ClaimsMachinesAlreadyUpdating(t *testing.T) {
	f := newInPlaceFixture(t, map[string]string{clusterv1.UpdateInProgressAnnotation: ""})

	assert.True(t, isUpdatingInPlace(f.machine))
}

func TestNextInPlaceCandidate_SkipsRotatedInfraTemplate(t *testing.T) {
	f := newInPlaceFixture(t, nil)

	// Point the TCP at a template the InfraMachine was not cloned from. Rotating an
	// infrastructure template needs a new InfraMachine, which only a replacement produces, so
	// claiming the machine would leave it outdated forever.
	f.controlPlane.infraObjects["machine-1"].SetAnnotations(map[string]string{
		clusterv1.TemplateClonedFromNameAnnotation:      "old-template",
		clusterv1.TemplateClonedFromGroupKindAnnotation: "TinkerbellMachineTemplate.infrastructure.cluster.x-k8s.io",
	})
	f.tcp.Spec.MachineTemplate.Spec.InfrastructureRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: "infrastructure.cluster.x-k8s.io",
		Kind:     "TinkerbellMachineTemplate",
		Name:     "new-template",
	}

	candidate := f.reconciler.nextInPlaceCandidate(f.controlPlane, collections.FromMachines(f.machine))

	assert.Nil(t, candidate, "a machine needing a new InfraMachine must be left to the rollout path")
}

func TestNextInPlaceCandidate_SkipsMachineWithoutNodeRef(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	f.machine.Status.NodeRef = clusterv1.MachineNodeReference{}

	assert.Nil(t, f.reconciler.nextInPlaceCandidate(f.controlPlane, collections.FromMachines(f.machine)))
}

func TestNextInPlaceCandidate_SkipsDeletingMachine(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	now := metav1.Now()
	f.machine.DeletionTimestamp = &now

	assert.Nil(t, f.reconciler.nextInPlaceCandidate(f.controlPlane, collections.FromMachines(f.machine)))
}

func TestCanUpdateMachine_CoveredDiffIsAccepted(t *testing.T) {
	f := newInPlaceFixture(t, nil)

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	ok, err := f.reconciler.canUpdateMachine(context.Background(), plan, f.controlPlane)
	require.NoError(t, err)
	assert.True(t, ok)
}

// Anything the extension leaves unpatched must send the machine down the rollout path.
func TestCanUpdateMachine_UncoveredDiffIsDeclined(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	f.caller.respond = func(_ *runtimehooksv1.CanUpdateMachineRequest, resp *runtimehooksv1.CanUpdateMachineResponse) {
		resp.Status = runtimehooksv1.ResponseStatusSuccess // no patches at all
	}

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	ok, err := f.reconciler.canUpdateMachine(context.Background(), plan, f.controlPlane)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCanUpdateMachine_NoExtensionRegistered(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	f.caller.extensions = nil

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	ok, err := f.reconciler.canUpdateMachine(context.Background(), plan, f.controlPlane)
	require.NoError(t, err)
	assert.False(t, ok)
}

// Cluster API supports exactly one extension per hook; more is an error rather than an
// arbitrary choice between them.
func TestCanUpdateMachine_MultipleExtensionsIsAnError(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	f.caller.extensions = []string{"a", "b"}

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	_, err = f.reconciler.canUpdateMachine(context.Background(), plan, f.controlPlane)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one is supported")
}

func TestBuildInPlacePlan_UsesDesiredSpecFromTCP(t *testing.T) {
	f := newInPlaceFixture(t, nil)
	f.tcp.Spec.ControlPlaneConfig.ControlPlaneConfig.TalosVersion = "v1.13"

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	assert.Equal(t, "v1.34.0", plan.desiredMachine.Spec.Version)
	assert.Equal(t, "v1.13", plan.desiredTalosConfig.Spec.TalosVersion)
	assert.Equal(t, "v1.33.0", plan.machine.Spec.Version, "the current machine must not be mutated")
}

// The trigger is what commits Cluster API to the in-place path, so all three objects must end
// up annotated and the hook must end up pending.
func TestTriggerInPlaceUpdate_AnnotatesAndMarksHookPending(t *testing.T) {
	f := newInPlaceFixture(t, nil)

	plan, err := f.reconciler.buildInPlacePlan(f.tcp, f.controlPlane, f.machine)
	require.NoError(t, err)

	require.NoError(t, f.reconciler.triggerInPlaceUpdate(context.Background(), plan))

	var machine clusterv1.Machine
	require.NoError(t, f.reconciler.Client.Get(context.Background(),
		ctrlKey("default", "machine-1"), &machine))

	assert.Contains(t, machine.Annotations, clusterv1.UpdateInProgressAnnotation)
	assert.Contains(t, machine.Annotations[runtimev1.PendingHooksAnnotation], "UpdateMachine")

	var talosConfig cabptv1.TalosConfig
	require.NoError(t, f.reconciler.Client.Get(context.Background(),
		ctrlKey("default", "machine-1"), &talosConfig))

	assert.Contains(t, talosConfig.Annotations, clusterv1.UpdateInProgressAnnotation,
		"CABPT's webhook admits the spec change only because of this annotation")
	assert.Equal(t, "controlplane", talosConfig.Spec.GenerateType)
}

// ctrlKey is a small helper to keep the object lookups above readable.
func ctrlKey(namespace, name string) client.ObjectKey {
	return client.ObjectKey{Namespace: namespace, Name: name}
}
