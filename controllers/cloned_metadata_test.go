// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilcache "k8s.io/apimachinery/pkg/util/cache"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestReconcileClonedObjectsMetadata checks that the labels and annotations of the machine template reach the
// infrastructure machine and the TalosConfig of an existing Machine, and that the ones removed from the template are
// removed from them, while the ones set by others are kept.
func TestReconcileClonedObjectsMetadata(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	scheme, cluster, tcp, objs := bootTestObjects(g)

	// The infrastructure machines are read through the contract, as the template.
	objs = append(objs, &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   contract.CalculateCRDName(bootTestTemplateGroup, "GenericInfrastructureMachine"),
			Labels: map[string]string{"cluster.x-k8s.io/v1beta2": "v1beta1_v1beta2"},
		},
	})

	tcp.Spec.MachineTemplate.ObjectMeta = clusterv1.ObjectMeta{
		Labels:      map[string]string{"example.siderolabs.dev/role": "cp", "example.siderolabs.dev/dropped": "true"},
		Annotations: map[string]string{"example.siderolabs.dev/note": "hello"},
	}

	applies := 0

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp)...).WithReturnManagedFields().
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				applies++

				return c.Apply(ctx, obj, opts...)
			},
		}).Build()

	// The objects are cloned without server-side apply, as the classic manager.
	const classicManager = "manager"

	r := &TalosControlPlaneReconciler{
		Client:         client.WithFieldOwner(fakeClient, classicManager),
		APIReader:      fakeClient,
		Log:            testr.New(t),
		Scheme:         scheme,
		applyCache:     utilcache.NewExpiring(),
		classicManager: classicManager,
	}

	infraTemplate, err := r.getInfrastructureTemplate(ctx, tcp)
	g.Expect(err).NotTo(HaveOccurred())

	_, err = r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster, infraTemplate: infraTemplate}, false)
	g.Expect(err).NotTo(HaveOccurred())

	infraMachines, talosConfigs := listBootTestClones(ctx, g, fakeClient)
	g.Expect(infraMachines).To(HaveLen(1))
	g.Expect(talosConfigs).To(HaveLen(1))

	// Another controller labels the cloned objects.
	infraMachine := &infraMachines[0]
	talosConfig := &talosConfigs[0]

	for _, obj := range []client.Object{infraMachine, talosConfig} {
		obj.SetLabels(map[string]string{"example.siderolabs.dev/other": "true"})

		g.Expect(fakeClient.Patch(ctx, obj, client.Merge, client.FieldOwner("other"))).To(Succeed())
	}

	reconcile := func() (*unstructured.Unstructured, *cabptv1.TalosConfig) {
		t.Helper()

		machines := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(ctx, machines, client.InNamespace(remediationTestNamespace))).To(Succeed())

		controlPlane, err := newControlPlane(ctx, fakeClient, cluster, tcp, collections.FromMachineList(machines))
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(r.reconcileClonedObjectsMetadata(ctx, controlPlane)).To(Succeed())

		infraMachines, talosConfigs := listBootTestClones(ctx, g, fakeClient)
		g.Expect(infraMachines).To(HaveLen(1))
		g.Expect(talosConfigs).To(HaveLen(1))

		return &infraMachines[0], &talosConfigs[0]
	}

	// The first reconcile adopts the objects: they are in sync with the machine template.
	applies = 0

	infraMachine, talosConfig = reconcile()
	g.Expect(applies).To(Equal(2))

	for _, obj := range []client.Object{infraMachine, talosConfig} {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/dropped", "true"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/other", "true"))
	}

	tcp.Spec.MachineTemplate.ObjectMeta = clusterv1.ObjectMeta{
		Labels:      map[string]string{"example.siderolabs.dev/role": "control-plane"},
		Annotations: map[string]string{"example.siderolabs.dev/note": "bye"},
	}

	infraMachine, talosConfig = reconcile()
	g.Expect(applies).To(Equal(4))

	for _, obj := range []client.Object{infraMachine, talosConfig} {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/role", "control-plane"))
		g.Expect(obj.GetLabels()).NotTo(HaveKey("example.siderolabs.dev/dropped"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/other", "true"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.ClusterNameLabel, cluster.Name))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.MachineControlPlaneNameLabel, tcp.Name))
		g.Expect(obj.GetAnnotations()).To(HaveKeyWithValue("example.siderolabs.dev/note", "bye"))
	}

	// The annotations recording the template the infrastructure machine was cloned from are kept: rollouts need them.
	g.Expect(infraMachine.GetAnnotations()).To(HaveKeyWithValue(clusterv1.TemplateClonedFromNameAnnotation, "infra"))

	// The objects in sync are not applied again.
	reconcile()
	g.Expect(applies).To(Equal(4))
}
