// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/pointer"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

func TestReconcileMachineTemplateStatePropagatesMachineFieldsInPlace(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cp-0",
			Namespace: "default",
			Labels: map[string]string{
				"keep": "true",
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "workload-cluster",
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(machine).Build()
	reconciler := &TalosControlPlaneReconciler{Client: fakeClient}

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
						NodeDrainTimeoutSeconds:        pointer.Int32(20),
						NodeVolumeDetachTimeoutSeconds: pointer.Int32(30),
						NodeDeletionTimeoutSeconds:     pointer.Int32(40),
					},
				},
			},
		},
	}

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
	if persisted.Labels[clusterv1.MachineControlPlaneNameLabel] == "" {
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
