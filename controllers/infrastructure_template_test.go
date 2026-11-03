// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// TestReconcileBacksOffOnMissingInfrastructureTemplate checks that a missing infrastructure template is reported
// in the MachinesCreated condition and requeued, instead of failing the reconcile.
func TestReconcileBacksOffOnMissingInfrastructureTemplate(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	// Only the metadata of the CRD is read, to resolve the API version of the template from its contract label.
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "genericinfrastructuremachinetemplates.infrastructure.cluster.x-k8s.io",
			Labels: map[string]string{
				clusterv1.GroupVersion.String(): "v1beta2",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
	reconciler := &TalosControlPlaneReconciler{Client: fakeClient}

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
		Status: clusterv1.ClusterStatus{
			Initialization: clusterv1.ClusterInitializationStatus{
				InfrastructureProvisioned: ptr.To(true),
			},
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "infrastructure.cluster.x-k8s.io",
						Kind:     "GenericInfrastructureMachineTemplate",
						Name:     "missing",
					},
				},
			},
		},
	}

	res, _, err := reconciler.reconcile(context.Background(), cluster, tcp)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.RequeueAfter).To(Equal(30 * time.Second))

	condition := apimeta.FindStatusCondition(tcp.Status.Conditions, string(controlplanev1.MachinesCreatedCondition))
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal(controlplanev1.InfrastructureTemplateCloningFailedReason))
}

// TestReconcileContinuesWithoutInfrastructureTemplate checks that a missing infrastructure template only stops the
// creation of Machines: the other steps still run, and the failure is still reported once they ran.
func TestReconcileContinuesWithoutInfrastructureTemplate(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	scheme, cluster, tcp, objs := bootTestObjects(g)
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "1.2.3.4", Port: 6443}
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	tcp.Spec.Replicas = ptr.To[int32](1)
	tcp.Spec.MachineTemplate.Spec.InfrastructureRef.Name = "missing"
	tcp.Spec.MachineTemplate.ObjectMeta.Labels = map[string]string{"example.siderolabs.dev/role": "cp"}

	machine := computeDesiredMachine(tcp, cluster, nil)
	machine.Name = "cp-0"
	delete(machine.Labels, "example.siderolabs.dev/role")

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp, machine)...).
		WithStatusSubresource(&clusterv1.Machine{}, &controlplanev1.TalosControlPlane{}).Build()

	r := &TalosControlPlaneReconciler{
		Client:       c,
		APIReader:    c,
		Log:          testr.New(t),
		Scheme:       scheme,
		ClusterCache: clustercache.NewFakeClusterCache(c, client.ObjectKeyFromObject(cluster)),
	}

	// A missing template backs off instead of failing the reconcile.
	res, _, err := r.reconcile(ctx, cluster, tcp)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.RequeueAfter).NotTo(BeZero())

	// The machine template is still propagated to the existing Machine.
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(machine), machine)).To(Succeed())
	g.Expect(machine.Labels).To(HaveKeyWithValue("example.siderolabs.dev/role", "cp"))

	condition := apimeta.FindStatusCondition(tcp.Status.Conditions, string(controlplanev1.MachinesCreatedCondition))
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal(controlplanev1.InfrastructureTemplateCloningFailedReason))
}
