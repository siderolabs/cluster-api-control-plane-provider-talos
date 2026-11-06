// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilcache "k8s.io/apimachinery/pkg/util/cache"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/labels/format"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

func TestReconcileMachineTemplateStatePropagatesMachineFieldsInPlace(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
			UID:       "tcp-uid",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"example.siderolabs.dev/control-plane": "true",
					},
					Annotations: map[string]string{
						"example.siderolabs.dev/annotation": "present",
					},
				},
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					ReadinessGates: []clusterv1.MachineReadinessGate{
						{ConditionType: "APIServerReady"},
					},
					Deletion: controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec{
						NodeDrainTimeoutSeconds:        ptr.To[int32](20),
						NodeVolumeDetachTimeoutSeconds: ptr.To[int32](30),
						NodeDeletionTimeoutSeconds:     ptr.To[int32](40),
					},
				},
			},
		},
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp-0",
			Namespace: "default",
			Labels: map[string]string{
				"keep": "true",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "workload-cluster",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine).Build()
	reconciler := &TalosControlPlaneReconciler{Client: fakeClient}

	machines := &clusterv1.MachineList{Items: []clusterv1.Machine{*machine.DeepCopy()}}

	if err := reconciler.reconcileMachineTemplateState(context.Background(), cluster, tcp, machines); err != nil {
		t.Fatalf("reconcileMachineTemplateState failed: %v", err)
	}

	persisted := &clusterv1.Machine{}
	if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(machine), persisted); err != nil {
		t.Fatalf("failed to get reconciled machine: %v", err)
	}

	if persisted.Labels["keep"] != "true" {
		t.Fatalf("expected unrelated labels to be preserved")
	}
	if persisted.Labels["example.siderolabs.dev/control-plane"] != "true" {
		t.Fatalf("expected machineTemplate labels to be propagated")
	}
	if persisted.Annotations["example.siderolabs.dev/annotation"] != "present" {
		t.Fatalf("expected machineTemplate annotations to be propagated")
	}
	if persisted.Labels[clusterv1.ClusterNameLabel] != cluster.Name {
		t.Fatalf("expected cluster label to be enforced")
	}
	if persisted.Labels[clusterv1.MachineControlPlaneLabel] != "" {
		t.Fatalf("expected control plane label to be enforced")
	}
	if persisted.Labels[clusterv1.MachineControlPlaneNameLabel] != tcp.Name {
		t.Fatalf("expected control plane name label to be set")
	}
	if len(persisted.Spec.ReadinessGates) != 1 || persisted.Spec.ReadinessGates[0].ConditionType != "APIServerReady" {
		t.Fatalf("expected readiness gates to be propagated")
	}
	if persisted.Spec.Deletion.NodeDrainTimeoutSeconds == nil || *persisted.Spec.Deletion.NodeDrainTimeoutSeconds != 20 {
		t.Fatalf("expected nodeDrainTimeout to be propagated")
	}
	if persisted.Spec.Deletion.NodeVolumeDetachTimeoutSeconds == nil || *persisted.Spec.Deletion.NodeVolumeDetachTimeoutSeconds != 30 {
		t.Fatalf("expected nodeVolumeDetachTimeout to be propagated")
	}
	if persisted.Spec.Deletion.NodeDeletionTimeoutSeconds == nil || *persisted.Spec.Deletion.NodeDeletionTimeoutSeconds != 40 {
		t.Fatalf("expected nodeDeletionTimeout to be propagated")
	}
	if persisted.Spec.InfrastructureRef != (clusterv1.ContractVersionedObjectReference{}) {
		t.Fatalf("expected reconcileMachineTemplateState to avoid mutating infrastructureRef")
	}
}

// TestControlPlaneMachineLabelsHashLongName checks that a TalosControlPlane name that is not a valid label value
// is hashed the same way as Cluster API does, so that its selectors match the control plane Machines.
func TestControlPlaneMachineLabelsHashLongName(t *testing.T) {
	t.Parallel()

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.Repeat("talos-control-plane-", 4),
		},
	}

	labels := controlPlaneMachineLabelsForCluster(tcp, "workload-cluster")

	if got, want := labels[clusterv1.MachineControlPlaneNameLabel], format.MustFormatValue(tcp.Name); got != want {
		t.Fatalf("expected control plane name label %q, got %q", want, got)
	}
}

// TestReconcileMachineTemplateStateRemovesDroppedFields checks that a Machine created before the controller used
// server-side apply is adopted, that a label removed from the machine template is removed from the Machine, and that
// a Machine in sync with the machine template is not applied again.
func TestReconcileMachineTemplateStateRemovesDroppedFields(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
			UID:       "tcp-uid",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{"example.siderolabs.dev/dropped": "true"},
				},
			},
		},
	}

	// The Machine as created by an earlier version of the controller, without server-side apply.
	machine := computeDesiredMachine(tcp, cluster, nil)
	machine.Name = "cp-0"
	machine.Labels["unmanaged"] = "true"

	applies := 0

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithReturnManagedFields().WithInterceptorFuncs(interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applies++

			return c.Apply(ctx, obj, opts...)
		},
	}).Build()

	// The binary is not named manager here: the classic manager comes from the user agent of the client.
	const classicManager = "cacppt-downstream"

	if err := fakeClient.Create(context.Background(), machine, client.FieldOwner(classicManager)); err != nil {
		t.Fatalf("failed to create machine: %v", err)
	}

	reconciler := &TalosControlPlaneReconciler{
		Client:            fakeClient,
		machineApplyCache: utilcache.NewExpiring(),
		classicManager:    classicManagerName(classicManager + "/v0.6.0 (linux/amd64) kubernetes/abcdef0"),
	}

	sync := func() *clusterv1.Machine {
		t.Helper()

		machines := &clusterv1.MachineList{}
		if err := fakeClient.List(context.Background(), machines); err != nil {
			t.Fatalf("failed to list machines: %v", err)
		}

		if err := reconciler.reconcileMachineTemplateState(context.Background(), cluster, tcp, machines); err != nil {
			t.Fatalf("reconcileMachineTemplateState failed: %v", err)
		}

		persisted := &clusterv1.Machine{}
		if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(machine), persisted); err != nil {
			t.Fatalf("failed to get reconciled machine: %v", err)
		}

		return persisted
	}

	persisted := sync()
	if applies != 1 {
		t.Fatalf("expected the adopted machine to be applied once, got %d applies", applies)
	}

	if persisted.Labels["example.siderolabs.dev/dropped"] != "true" {
		t.Fatalf("expected the machine template label to be kept while it is in the template")
	}

	tcp.Spec.MachineTemplate.ObjectMeta.Labels = nil

	persisted = sync()
	if applies != 2 {
		t.Fatalf("expected the changed machine template to be applied, got %d applies", applies)
	}

	if _, ok := persisted.Labels["example.siderolabs.dev/dropped"]; ok {
		t.Fatalf("expected the label removed from the machine template to be removed from the machine")
	}

	if persisted.Labels["unmanaged"] != "true" {
		t.Fatalf("expected the label not set by the controller to be kept")
	}

	if persisted.Labels[clusterv1.ClusterNameLabel] != cluster.Name {
		t.Fatalf("expected the cluster label to be kept")
	}

	sync()

	if applies != 2 {
		t.Fatalf("expected the machine in sync not to be applied again, got %d applies", applies)
	}
}

// TestReconcileMachineTemplateStateSkipsMachinesItDoesNotControl checks that the control plane Machines of the
// cluster that the TalosControlPlane does not control are left as they are: applying them would adopt a Machine
// without a controller, and fail for a Machine with another one.
func TestReconcileMachineTemplateStateSkipsMachinesItDoesNotControl(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
			UID:       "tcp-uid",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{"example.siderolabs.dev/template": "true"},
				},
			},
		},
	}

	otherController := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-control-plane",
			Namespace: "default",
			UID:       "other-uid",
		},
	}

	newMachine := func(name string, ownerReferences ...metav1.OwnerReference) *clusterv1.Machine {
		return &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "",
				},
				OwnerReferences: ownerReferences,
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: cluster.Name,
			},
		}
	}

	unowned := newMachine("unowned")
	controlledByOther := newMachine("controlled-by-other",
		*metav1.NewControllerRef(otherController, controlplanev1.GroupVersion.WithKind("TalosControlPlane")))

	applies := 0

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(unowned, controlledByOther).WithInterceptorFuncs(interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applies++

			return c.Apply(ctx, obj, opts...)
		},
	}).Build()

	reconciler := &TalosControlPlaneReconciler{Client: fakeClient}

	machines := &clusterv1.MachineList{}
	if err := fakeClient.List(context.Background(), machines); err != nil {
		t.Fatalf("failed to list machines: %v", err)
	}

	if err := reconciler.reconcileMachineTemplateState(context.Background(), cluster, tcp, machines); err != nil {
		t.Fatalf("reconcileMachineTemplateState failed: %v", err)
	}

	if applies != 0 {
		t.Fatalf("expected no machine to be applied, got %d applies", applies)
	}

	for _, machine := range []*clusterv1.Machine{unowned, controlledByOther} {
		persisted := &clusterv1.Machine{}
		if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(machine), persisted); err != nil {
			t.Fatalf("failed to get machine %s: %v", machine.Name, err)
		}

		if _, ok := persisted.Labels["example.siderolabs.dev/template"]; ok {
			t.Fatalf("expected the machine template not to be applied to machine %s", machine.Name)
		}

		if len(persisted.OwnerReferences) != len(machine.OwnerReferences) {
			t.Fatalf("expected the owner references of machine %s to be kept, got %v", machine.Name, persisted.OwnerReferences)
		}
	}
}

// TestReconcileMachineTemplateStateResetsMinReadySecondsOnAdoption checks that adopting a Machine created before the
// controller used server-side apply resets its minReadySeconds to the TalosControlPlane, as the earlier sync did,
// instead of leaving a value the TalosControlPlane no longer sets, and that the apply owns it afterwards.
func TestReconcileMachineTemplateStateResetsMinReadySecondsOnAdoption(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name            string
		minReadySeconds *int32
	}{
		{name: "removed from the TalosControlPlane"},
		{name: "changed on the TalosControlPlane", minReadySeconds: ptr.To[int32](30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(clusterv1.AddToScheme(scheme))
			utilruntime.Must(controlplanev1.AddToScheme(scheme))

			cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "workload-cluster", Namespace: "default"}}

			tcp := &controlplanev1.TalosControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "talos-control-plane", Namespace: "default", UID: "tcp-uid"},
			}

			// The Machine as created by an earlier version of the controller, without server-side apply.
			machine := computeDesiredMachine(tcp, cluster, nil)
			machine.Name = "cp-0"
			machine.Spec.MinReadySeconds = ptr.To[int32](60)

			const classicManager = "manager"

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithReturnManagedFields().Build()
			if err := fakeClient.Create(context.Background(), machine, client.FieldOwner(classicManager)); err != nil {
				t.Fatalf("failed to create machine: %v", err)
			}

			reconciler := &TalosControlPlaneReconciler{Client: fakeClient, classicManager: classicManager}

			sync := func() *clusterv1.Machine {
				t.Helper()

				machines := &clusterv1.MachineList{}
				if err := fakeClient.List(context.Background(), machines); err != nil {
					t.Fatalf("failed to list machines: %v", err)
				}

				if err := reconciler.reconcileMachineTemplateState(context.Background(), cluster, tcp, machines); err != nil {
					t.Fatalf("reconcileMachineTemplateState failed: %v", err)
				}

				persisted := &clusterv1.Machine{}
				if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(machine), persisted); err != nil {
					t.Fatalf("failed to get reconciled machine: %v", err)
				}

				return persisted
			}

			tcp.Spec.MinReadySeconds = tc.minReadySeconds

			if got := sync().Spec.MinReadySeconds; !ptr.Equal(got, tc.minReadySeconds) {
				t.Fatalf("expected minReadySeconds %v after the adoption, got %v", ptr.Deref(tc.minReadySeconds, -1), ptr.Deref(got, -1))
			}

			// After the adoption, the apply removes minReadySeconds once the TalosControlPlane stops setting it.
			tcp.Spec.MinReadySeconds = nil

			if got := sync().Spec.MinReadySeconds; got != nil {
				t.Fatalf("expected minReadySeconds to be removed, got %d", *got)
			}
		})
	}
}

// TestSyncDeletingMachinesSyncsDeletionTimeouts checks that the deletion timeouts set on the TalosControlPlane reach
// a Machine that is already being deleted, so that a drain blocked by a PodDisruptionBudget can be ended from the
// TalosControlPlane, that no other field of that Machine is applied, that a Machine already in sync is not patched
// again, and that the timeouts the TalosControlPlane doesn't set are left as they are on the Machine.
func TestSyncDeletingMachinesSyncsDeletionTimeouts(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
			UID:       "tcp-uid",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{"example.siderolabs.dev/new": "true"},
				},
			},
		},
	}

	// A Machine created without timeouts, defaulted by the Machine webhook, and stuck in deletion.
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cp-0",
			Namespace:         "default",
			Labels:            map[string]string{"keep": "true"},
			Finalizers:        []string{clusterv1.MachineFinalizer},
			DeletionTimestamp: ptr.To(metav1.Now()),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "workload-cluster",
			Deletion: clusterv1.MachineDeletionSpec{
				NodeDeletionTimeoutSeconds: ptr.To[int32](10),
			},
		},
	}

	applies, patches := 0, 0

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine).WithInterceptorFuncs(interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applies++

			return c.Apply(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++

			return c.Patch(ctx, obj, patch, opts...)
		},
	}).Build()

	reconciler := &TalosControlPlaneReconciler{Client: fakeClient, classicManager: "manager", machineApplyCache: utilcache.NewExpiring()}

	sync := func() *clusterv1.Machine {
		t.Helper()

		machines := &clusterv1.MachineList{}
		if err := fakeClient.List(context.Background(), machines); err != nil {
			t.Fatalf("failed to list machines: %v", err)
		}

		if err := reconciler.syncDeletingMachines(context.Background(), tcp, machines); err != nil {
			t.Fatalf("syncDeletingMachines failed: %v", err)
		}

		if err := reconciler.reconcileMachineTemplateState(context.Background(), cluster, tcp, machines); err != nil {
			t.Fatalf("reconcileMachineTemplateState failed: %v", err)
		}

		persisted := &clusterv1.Machine{}
		if err := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(machine), persisted); err != nil {
			t.Fatalf("failed to get reconciled machine: %v", err)
		}

		return persisted
	}

	// Without timeouts on the TalosControlPlane, the defaulted Machine is left alone.
	sync()

	if patches != 0 || applies != 0 {
		t.Fatalf("expected a deleting machine in sync not to be updated, got %d patches and %d applies", patches, applies)
	}

	tcp.Spec.MachineTemplate.Spec.Deletion = controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec{
		NodeDrainTimeoutSeconds:        ptr.To[int32](600),
		NodeVolumeDetachTimeoutSeconds: ptr.To[int32](300),
		NodeDeletionTimeoutSeconds:     ptr.To[int32](60),
	}

	persisted := sync()

	if patches != 1 || applies != 0 {
		t.Fatalf("expected the deleting machine to be patched once and never applied, got %d patches and %d applies", patches, applies)
	}

	if got := persisted.Spec.Deletion.NodeDrainTimeoutSeconds; got == nil || *got != 600 {
		t.Fatalf("expected nodeDrainTimeoutSeconds 600 on the deleting machine, got %v", got)
	}

	if got := persisted.Spec.Deletion.NodeVolumeDetachTimeoutSeconds; got == nil || *got != 300 {
		t.Fatalf("expected nodeVolumeDetachTimeoutSeconds 300 on the deleting machine, got %v", got)
	}

	if got := persisted.Spec.Deletion.NodeDeletionTimeoutSeconds; got == nil || *got != 60 {
		t.Fatalf("expected nodeDeletionTimeoutSeconds 60 on the deleting machine, got %v", got)
	}

	if persisted.Labels["keep"] != "true" || persisted.Labels["example.siderolabs.dev/new"] != "" {
		t.Fatalf("expected the labels of the deleting machine to be left alone, got %v", persisted.Labels)
	}

	sync()

	if patches != 1 {
		t.Fatalf("expected a deleting machine in sync not to be patched again, got %d patches", patches)
	}

	// Removing the timeouts from the TalosControlPlane leaves them on the Machine, as well as a timeout set on the
	// Machine by hand.
	tcp.Spec.MachineTemplate.Spec.Deletion = controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec{}

	persisted.Spec.Deletion.NodeDrainTimeoutSeconds = ptr.To[int32](30)
	if err := fakeClient.Update(context.Background(), persisted); err != nil {
		t.Fatalf("failed to update the deleting machine: %v", err)
	}

	patches = 0

	persisted = sync()

	if patches != 0 {
		t.Fatalf("expected the deleting machine not to be patched when the TalosControlPlane sets no timeouts, got %d patches", patches)
	}

	if got := persisted.Spec.Deletion.NodeDrainTimeoutSeconds; got == nil || *got != 30 {
		t.Fatalf("expected the nodeDrainTimeoutSeconds set by hand to be kept, got %v", got)
	}

	if got := persisted.Spec.Deletion.NodeVolumeDetachTimeoutSeconds; got == nil || *got != 300 {
		t.Fatalf("expected nodeVolumeDetachTimeoutSeconds 300 to be kept, got %v", got)
	}
}

// deletionTimeoutTestObjects returns a TalosControlPlane that sets a drain timeout, and its control plane Machines,
// the ones named in deleting being deleted.
func deletionTimeoutTestObjects(names []string, deleting map[string]bool) (*controlplanev1.TalosControlPlane, []client.Object) {
	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
			UID:       "tcp-uid",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					Deletion: controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec{
						NodeDrainTimeoutSeconds: ptr.To[int32](60),
					},
				},
			},
		},
	}

	objects := make([]client.Object, 0, len(names))

	for _, name := range names {
		machine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         "workload-cluster",
					clusterv1.MachineControlPlaneLabel: "",
				},
				Finalizers: []string{clusterv1.MachineFinalizer},
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
				},
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: "workload-cluster",
			},
		}

		if deleting[name] {
			machine.DeletionTimestamp = ptr.To(metav1.Now())
		}

		objects = append(objects, machine)
	}

	return tcp, objects
}

// TestSyncDeletingMachinesContinuesAfterAFailure checks that a deleting Machine that fails to sync doesn't stop the
// other deleting Machines from getting the deletion timeouts, that the failure is reported, and that the failed
// Machine is not changed in memory.
func TestSyncDeletingMachinesContinuesAfterAFailure(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	tcp, objects := deletionTimeoutTestObjects([]string{"cp-0", "cp-1"}, map[string]bool{"cp-0": true, "cp-1": true})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if obj.GetName() == "cp-0" {
				return errors.New("admission webhook timed out")
			}

			return c.Patch(ctx, obj, patch, opts...)
		},
	}).Build()

	reconciler := &TalosControlPlaneReconciler{Client: fakeClient, classicManager: "manager"}

	machines := &clusterv1.MachineList{}
	if err := fakeClient.List(context.Background(), machines); err != nil {
		t.Fatalf("failed to list machines: %v", err)
	}

	err := reconciler.syncDeletingMachines(context.Background(), tcp, machines)
	if err == nil || !strings.Contains(err.Error(), "cp-0") {
		t.Fatalf("expected the failure of cp-0 to be reported, got %v", err)
	}

	for _, machine := range machines.Items {
		switch machine.Name {
		case "cp-0":
			if machine.Spec.Deletion.NodeDrainTimeoutSeconds != nil {
				t.Fatalf("expected the machine that failed to sync not to be changed in memory, got %v", *machine.Spec.Deletion.NodeDrainTimeoutSeconds)
			}
		case "cp-1":
			if got := machine.Spec.Deletion.NodeDrainTimeoutSeconds; got == nil || *got != 60 {
				t.Fatalf("expected nodeDrainTimeoutSeconds 60 in memory on cp-1, got %v", got)
			}
		}
	}

	persisted := &clusterv1.Machine{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "cp-1"}, persisted); err != nil {
		t.Fatalf("failed to get cp-1: %v", err)
	}

	if got := persisted.Spec.Deletion.NodeDrainTimeoutSeconds; got == nil || *got != 60 {
		t.Fatalf("expected nodeDrainTimeoutSeconds 60 on cp-1 despite the failure of cp-0, got %v", got)
	}
}

// TestReconcileDeleteSyncsDeletionTimeouts checks that the deletion timeouts reach the Machines already being
// deleted while the TalosControlPlane itself is being deleted, so that a drain blocking the teardown of the cluster
// can be ended from the TalosControlPlane, and that the other Machines are still deleted.
func TestReconcileDeleteSyncsDeletionTimeouts(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
	}

	tcp, objects := deletionTimeoutTestObjects([]string{"cp-0", "cp-1"}, map[string]bool{"cp-0": true})

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	reconciler := &TalosControlPlaneReconciler{Client: fakeClient, Log: testr.New(t), classicManager: "manager"}

	res, err := reconciler.reconcileDelete(context.Background(), cluster, tcp)
	if err != nil {
		t.Fatalf("reconcileDelete failed: %v", err)
	}

	if res.RequeueAfter == 0 {
		t.Fatalf("expected the deletion to be requeued, got %+v", res)
	}

	deleting := &clusterv1.Machine{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "cp-0"}, deleting); err != nil {
		t.Fatalf("failed to get cp-0: %v", err)
	}

	if got := deleting.Spec.Deletion.NodeDrainTimeoutSeconds; got == nil || *got != 60 {
		t.Fatalf("expected nodeDrainTimeoutSeconds 60 on the deleting machine, got %v", got)
	}

	other := &clusterv1.Machine{}
	if err := fakeClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "cp-1"}, other); err != nil {
		t.Fatalf("failed to get cp-1: %v", err)
	}

	if other.DeletionTimestamp.IsZero() {
		t.Fatal("expected the other machine to be deleted")
	}
}
