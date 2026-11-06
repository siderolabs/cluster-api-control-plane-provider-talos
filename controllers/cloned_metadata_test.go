// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilcache "k8s.io/apimachinery/pkg/util/cache"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// clonedMetadataTest boots a Machine whose infrastructure machine and TalosConfig get the labels and annotations of
// the machine template, and counts the applies and patches sent.
type clonedMetadataTest struct {
	g       *WithT
	client  client.Client
	r       *TalosControlPlaneReconciler
	cluster *clusterv1.Cluster
	tcp     *controlplanev1.TalosControlPlane
	applies int
	patches int

	// patchErr, if set, injects an error into the patches.
	patchErr func(obj client.Object) error
}

func newClonedMetadataTest(t *testing.T) *clonedMetadataTest {
	t.Helper()

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

	test := &clonedMetadataTest{g: g, cluster: cluster, tcp: tcp}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp)...).WithReturnManagedFields().
		WithInterceptorFuncs(interceptor.Funcs{
			Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
				test.applies++

				return c.Apply(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				test.patches++

				if test.patchErr != nil {
					if err := test.patchErr(obj); err != nil {
						return err
					}
				}

				return c.Patch(ctx, obj, patch, opts...)
			},
		}).Build()

	test.client = fakeClient
	test.r = &TalosControlPlaneReconciler{
		Client:         client.WithFieldOwner(fakeClient, legacyClonesManager),
		APIReader:      fakeClient,
		Log:            testr.New(t),
		Scheme:         scheme,
		applyCache:     utilcache.NewExpiring(),
		classicManager: legacyClonesManager,
	}

	infraTemplate, err := test.r.getInfrastructureTemplate(ctx, tcp)
	g.Expect(err).NotTo(HaveOccurred())

	_, err = test.r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster, infraTemplate: infraTemplate}, false)
	g.Expect(err).NotTo(HaveOccurred())

	return test
}

// legacyClonesManager is the classic manager of the controller, and of the providers that write to the cloned objects.
const legacyClonesManager = "manager"

// clones returns the infrastructure machine and the TalosConfig of the Machine.
func (test *clonedMetadataTest) clones() []client.Object {
	infraMachines, talosConfigs := listBootTestClones(context.Background(), test.g, test.client)
	test.g.Expect(infraMachines).To(HaveLen(1))
	test.g.Expect(talosConfigs).To(HaveLen(1))

	return []client.Object{&infraMachines[0], &talosConfigs[0]}
}

// reconcile builds the ControlPlane and propagates the machine template metadata to the cloned objects.
func (test *clonedMetadataTest) reconcile() []client.Object {
	ctx := context.Background()

	machines := &clusterv1.MachineList{}
	test.g.Expect(test.client.List(ctx, machines, client.InNamespace(remediationTestNamespace))).To(Succeed())

	controlPlane, err := newControlPlane(ctx, test.client, test.cluster, test.tcp, collections.FromMachineList(machines))
	test.g.Expect(err).NotTo(HaveOccurred())

	result, err := test.r.reconcileClonedObjectsMetadata(ctx, controlPlane)
	test.g.Expect(err).NotTo(HaveOccurred())
	test.g.Expect(result.IsZero()).To(BeTrue())

	return test.clones()
}

// cloneLikeEarlierRelease clones the objects of the Machine again the way an earlier release did, without
// server-side apply, and has a provider that shares the classic manager name label them.
func (test *clonedMetadataTest) cloneLikeEarlierRelease() {
	g := test.g
	ctx := context.Background()

	for _, obj := range test.clones() {
		g.Expect(test.client.Delete(ctx, obj)).To(Succeed())

		obj.SetResourceVersion("")
		obj.SetUID("")
		obj.SetManagedFields(nil)

		g.Expect(test.client.Create(ctx, obj, client.FieldOwner(legacyClonesManager))).To(Succeed())

		obj.SetLabels(map[string]string{"example.siderolabs.dev/provider": "true"})

		g.Expect(test.client.Patch(ctx, obj, client.Merge, client.FieldOwner(legacyClonesManager))).To(Succeed())
		g.Expect(obj.GetManagedFields()).To(HaveLen(1))
	}
}

// ownedMetadata returns the label and annotation keys the field manager owns on the object with the operation.
func ownedMetadata(g *WithT, obj client.Object, manager string, operation metav1.ManagedFieldsOperationType) (labels, annotations []string) {
	for _, managedField := range obj.GetManagedFields() {
		if managedField.Manager != manager || managedField.Operation != operation || managedField.Subresource != "" {
			continue
		}

		fields := map[string]any{}
		g.Expect(json.Unmarshal(managedField.FieldsV1.GetRawBytes(), &fields)).To(Succeed())

		metadata, _ := fields["f:metadata"].(map[string]any)

		for section, keys := range map[string]*[]string{"f:labels": &labels, "f:annotations": &annotations} {
			owned, _ := metadata[section].(map[string]any)
			for key := range owned {
				// "." is the map itself.
				if key != "." {
					*keys = append(*keys, strings.TrimPrefix(key, "f:"))
				}
			}
		}
	}

	return labels, annotations
}

// TestReconcileClonedObjectsMetadata checks that the labels and annotations of the machine template reach the
// infrastructure machine and the TalosConfig of an existing Machine, and that the ones removed from the template are
// removed from them, while the ones set by others are kept.
func TestReconcileClonedObjectsMetadata(t *testing.T) {
	test := newClonedMetadataTest(t)
	g := test.g
	ctx := context.Background()

	// The objects are created with server-side apply: the metadata field manager owns exactly the labels and
	// annotations of the machine template, and the creating field manager none of them.
	for _, obj := range test.clones() {
		labels, annotations := ownedMetadata(g, obj, tcpMetadataManagerName, metav1.ManagedFieldsOperationApply)
		g.Expect(labels).To(ConsistOf("example.siderolabs.dev/role", "example.siderolabs.dev/dropped",
			clusterv1.ClusterNameLabel, clusterv1.MachineControlPlaneLabel, clusterv1.MachineControlPlaneNameLabel))
		g.Expect(annotations).To(ConsistOf("example.siderolabs.dev/note"))

		labels, annotations = ownedMetadata(g, obj, tcpManagerName, metav1.ManagedFieldsOperationApply)
		g.Expect(labels).To(BeEmpty())
		g.Expect(annotations).NotTo(ContainElement("example.siderolabs.dev/note"))

		labels, annotations = ownedMetadata(g, obj, legacyClonesManager, metav1.ManagedFieldsOperationUpdate)
		g.Expect(labels).To(BeEmpty())
		g.Expect(annotations).To(BeEmpty())
	}

	// Another controller labels the cloned objects.
	for _, obj := range test.clones() {
		obj.SetLabels(map[string]string{"example.siderolabs.dev/other": "true"})

		g.Expect(test.client.Patch(ctx, obj, client.Merge, client.FieldOwner("other"))).To(Succeed())
	}

	// The objects created with server-side apply are not migrated.
	test.patches = 0

	test.reconcile()
	g.Expect(test.patches).To(BeZero())

	test.tcp.Spec.MachineTemplate.ObjectMeta = clusterv1.ObjectMeta{
		Labels:      map[string]string{"example.siderolabs.dev/role": "control-plane"},
		Annotations: map[string]string{"example.siderolabs.dev/note": "bye"},
	}

	clones := test.reconcile()

	for _, obj := range clones {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/role", "control-plane"))
		g.Expect(obj.GetLabels()).NotTo(HaveKey("example.siderolabs.dev/dropped"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/other", "true"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.ClusterNameLabel, test.cluster.Name))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.MachineControlPlaneNameLabel, test.tcp.Name))
		g.Expect(obj.GetAnnotations()).To(HaveKeyWithValue("example.siderolabs.dev/note", "bye"))
	}

	// The annotations recording the template the infrastructure machine was cloned from are kept: rollouts need them.
	g.Expect(clones[0].GetAnnotations()).To(HaveKeyWithValue(clusterv1.TemplateClonedFromNameAnnotation, "infra"))

	// The objects in sync are not applied again.
	applies := test.applies

	test.reconcile()
	g.Expect(test.applies).To(Equal(applies))

	// Not even after their providers wrote to them, which changes their resource version.
	for _, obj := range test.clones() {
		resourceVersion := obj.GetResourceVersion()

		obj.SetAnnotations(map[string]string{"example.siderolabs.dev/provider-state": "provisioned"})

		g.Expect(test.client.Patch(ctx, obj, client.Merge, client.FieldOwner("provider"))).To(Succeed())
		g.Expect(obj.GetResourceVersion()).NotTo(Equal(resourceVersion))
	}

	test.reconcile()
	g.Expect(test.applies).To(Equal(applies))

	// A label of the machine template changed by another controller is set back.
	for _, obj := range test.clones() {
		obj.SetLabels(map[string]string{"example.siderolabs.dev/role": "worker"})

		g.Expect(test.client.Patch(ctx, obj, client.Merge, client.FieldOwner("other"))).To(Succeed())
	}

	for _, obj := range test.reconcile() {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/role", "control-plane"))
	}

	g.Expect(test.applies).To(Equal(applies + 2))
}

// TestReconcileClonedObjectsMetadataMigration checks that the objects cloned by an earlier release, without
// server-side apply, are adopted once: the metadata field manager takes over the labels and annotations of the
// machine template, while the other fields of the classic manager, which the providers may share, keep their owner.
func TestReconcileClonedObjectsMetadataMigration(t *testing.T) {
	test := newClonedMetadataTest(t)
	g := test.g

	test.cloneLikeEarlierRelease()

	test.tcp.Spec.MachineTemplate.ObjectMeta = clusterv1.ObjectMeta{
		Labels:      map[string]string{"example.siderolabs.dev/role": "control-plane"},
		Annotations: map[string]string{"example.siderolabs.dev/note": "bye"},
	}

	// The first reconcile adopts the objects. The label removed from the machine template before is not among the
	// keys handed over: it stays, owned by the classic manager.
	test.patches = 0

	clones := test.reconcile()

	for _, obj := range clones {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/role", "control-plane"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/dropped", "true"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/provider", "true"))
		g.Expect(obj.GetAnnotations()).To(HaveKeyWithValue("example.siderolabs.dev/note", "bye"))

		labels, annotations := ownedMetadata(g, obj, legacyClonesManager, metav1.ManagedFieldsOperationUpdate)
		g.Expect(labels).To(ConsistOf("example.siderolabs.dev/dropped", "example.siderolabs.dev/provider"))
		g.Expect(annotations).NotTo(ContainElement("example.siderolabs.dev/note"))

		g.Expect(slices.ContainsFunc(obj.GetManagedFields(), func(managedField metav1.ManagedFieldsEntry) bool {
			return managedField.Manager == legacyClonesManager && strings.Contains(string(managedField.FieldsV1.GetRawBytes()), `"f:ownerReferences"`)
		})).To(BeTrue(), "the owner references of the classic manager keep their owner")
	}

	// The fake client applies the typed TalosConfig as a whole, so only the infrastructure machine shows that the spec
	// fields of the classic manager keep their owner.
	g.Expect(slices.ContainsFunc(clones[0].GetManagedFields(), func(managedField metav1.ManagedFieldsEntry) bool {
		return managedField.Manager == legacyClonesManager && strings.Contains(string(managedField.FieldsV1.GetRawBytes()), `"f:hello"`)
	})).To(BeTrue(), "the spec fields of the classic manager keep their owner")

	g.Expect(test.patches).To(Equal(2))

	// The objects are adopted once, and the labels removed from the machine template since are removed.
	test.tcp.Spec.MachineTemplate.ObjectMeta.Labels = nil
	test.patches = 0

	for _, obj := range test.reconcile() {
		g.Expect(obj.GetLabels()).NotTo(HaveKey("example.siderolabs.dev/role"))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/provider", "true"))
	}

	g.Expect(test.patches).To(BeZero())
}

// TestReconcileClonedObjectsMetadataConflict checks that a cloned object whose managed fields migration conflicts,
// as it was read from a stale cache, is migrated again on a requeue instead of failing the reconcile, and that the
// other cloned objects are still applied.
func TestReconcileClonedObjectsMetadataConflict(t *testing.T) {
	test := newClonedMetadataTest(t)
	g := test.g
	ctx := context.Background()

	test.cloneLikeEarlierRelease()

	test.patchErr = func(obj client.Object) error {
		if _, ok := obj.(*cabptv1.TalosConfig); ok {
			return apierrors.NewConflict(cabptv1.GroupVersion.WithResource("talosconfigs").GroupResource(), obj.GetName(), errors.New("the object has been modified"))
		}

		return nil
	}

	machines := &clusterv1.MachineList{}
	g.Expect(test.client.List(ctx, machines, client.InNamespace(remediationTestNamespace))).To(Succeed())

	controlPlane, err := newControlPlane(ctx, test.client, test.cluster, test.tcp, collections.FromMachineList(machines))
	g.Expect(err).NotTo(HaveOccurred())

	result, err := test.r.reconcileClonedObjectsMetadata(ctx, controlPlane)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(clonedObjectConflictRequeue))

	clones := test.clones()
	g.Expect(hasApplyManagedFields(clones[0], tcpMetadataManagerName)).To(BeTrue())
	g.Expect(hasApplyManagedFields(clones[1], tcpMetadataManagerName)).To(BeFalse())

	// The requeue migrates it.
	test.patchErr = nil

	test.reconcile()
	g.Expect(hasApplyManagedFields(test.clones()[1], tcpMetadataManagerName)).To(BeTrue())

	// Other errors are still reported.
	test.cloneLikeEarlierRelease()

	errInjected := errors.New("injected failure")
	test.patchErr = func(client.Object) error { return errInjected }

	controlPlane, err = newControlPlane(ctx, test.client, test.cluster, test.tcp, collections.FromMachineList(machines))
	g.Expect(err).NotTo(HaveOccurred())

	_, err = test.r.reconcileClonedObjectsMetadata(ctx, controlPlane)
	g.Expect(err).To(MatchError(ContainSubstring(errInjected.Error())))
}
