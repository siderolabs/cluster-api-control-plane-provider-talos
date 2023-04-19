// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/types/v1alpha1/generate"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/controllers"
)

type ControllersSuite struct {
	suite.Suite

	machineService *machineService
	secretsBundle  *generate.SecretsBundle
	machineAddress string
	ctx            context.Context
	cancel         context.CancelFunc
}

func (suite *ControllersSuite) SetupSuite() {
	var err error

	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), time.Minute*10)

	suite.secretsBundle, err = generate.NewSecretsBundle(generate.NewClock(), generate.WithEndpointList([]string{"127.0.0.1"}))
	suite.Require().NoError(err)

	suite.machineService, suite.machineAddress, err = startMachineServer(suite.ctx, suite.secretsBundle)
	suite.Require().NoError(err)
}

func (suite *ControllersSuite) TearDownSuite() {
	suite.cancel()
}

func (suite *ControllersSuite) TestClusterToTalosControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneRef: &corev1.ObjectReference{
			Kind:       "TalosControlPlane",
			Namespace:  metav1.NamespaceDefault,
			Name:       "tcp-foo",
			APIVersion: controlplanev1.GroupVersion.String(),
		},
	}

	expectedResult := []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: cluster.Spec.ControlPlaneRef.Namespace,
				Name:      cluster.Spec.ControlPlaneRef.Name},
		},
	}

	r := newReconciler(fakeClient)

	got := r.ClusterToTalosControlPlane(cluster)
	g.Expect(got).To(Equal(expectedResult))
}

func (suite *ControllersSuite) TestClusterToTalosControlPlaneNoControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	r := newReconciler(fakeClient)

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})

	got := r.ClusterToTalosControlPlane(cluster)
	g.Expect(got).To(BeNil())
}

func (suite *ControllersSuite) TestClusterToTalosControlPlaneOtherControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneRef: &corev1.ObjectReference{
			Kind:       "OtherControlPlane",
			Namespace:  metav1.NamespaceDefault,
			Name:       "other-foo",
			APIVersion: controlplanev1.GroupVersion.String(),
		},
	}

	r := &controllers.TalosControlPlaneReconciler{
		Client: fakeClient,
	}

	got := r.ClusterToTalosControlPlane(cluster)
	g.Expect(got).To(BeNil())
}

func (suite *ControllersSuite) TestReconcilePaused() {
	g := NewWithT(suite.T())

	clusterName := "foo"

	// Test: cluster is paused and tcp is not
	cluster := newCluster(&types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: clusterName})
	cluster.Spec.Paused = true
	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceDefault,
			Name:      clusterName,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       clusterName,
				},
			},
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.16.6",
		},
	}
	tcp.Default()
	g.Expect(tcp.ValidateCreate()).To(Succeed())
	fakeClient := newFakeClient(tcp.DeepCopy(), cluster.DeepCopy())
	r := newReconciler(fakeClient)

	_, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	machineList := &clusterv1.MachineList{}
	g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(metav1.NamespaceDefault))).To(Succeed())
	g.Expect(machineList.Items).To(BeEmpty())

	// Test: tcp is paused and cluster is not
	cluster.Spec.Paused = false
	tcp.ObjectMeta.Annotations = map[string]string{}
	tcp.ObjectMeta.Annotations[clusterv1.PausedAnnotation] = "paused"
	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
}

func (suite *ControllersSuite) TestReconcileClusterNoEndpoints() {
	g := NewWithT(suite.T())

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Status = clusterv1.ClusterStatus{InfrastructureReady: true}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      "foo",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       cluster.Name,
				},
			},
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.16.6",
			InfrastructureTemplate: corev1.ObjectReference{
				Kind:       "UnknownInfraMachine",
				APIVersion: "test/v1alpha1",
				Name:       "foo",
				Namespace:  metav1.NamespaceDefault,
			},
		},
	}

	tcp.Default()
	g.Expect(tcp.ValidateCreate()).To(Succeed())

	ca := corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: corev1.SchemeGroupVersion.Version,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-ca",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"talosconfig": make([]byte, 0),
		},
	}

	fakeClient := newFakeClient(tcp.DeepCopy(), cluster.DeepCopy(), ca.DeepCopy())
	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	result, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	// this first requeue is to add finalizer
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(r.Client.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	result, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(result).To(Equal(ctrl.Result{Requeue: false, RequeueAfter: 20 * time.Second}))
	g.Expect(r.Client.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())

	// Always expect that the Finalizer is set on the passed in resource
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	g.Expect(tcp.Status.Selector).NotTo(BeEmpty())

	_, err = secret.GetFromNamespacedName(suite.ctx, fakeClient, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: "foo"}, secret.ClusterCA)
	g.Expect(err).NotTo(HaveOccurred())

	machineList := &clusterv1.MachineList{}
	g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(metav1.NamespaceDefault))).To(Succeed())
	g.Expect(machineList.Items).To(BeEmpty())
}

func (suite *ControllersSuite) TestReconcileInitializeControlPlane() {
	t := suite.T()

	fakeClient := newFakeClient()

	setup := func(t *testing.T, g *WithT) *corev1.Namespace {
		t.Helper()

		t.Log("Creating the namespace")

		ns := &corev1.Namespace{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1.SchemeGroupVersion.Version,
				Kind:       "Namespace",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-reconcile-initialize",
			},
		}

		g.Expect(fakeClient.Create(suite.ctx, ns)).To(Succeed())

		return ns
	}

	teardown := func(t *testing.T, g *WithT, ns *corev1.Namespace) {
		t.Helper()

		t.Log("Deleting the namespace")
		g.Expect(fakeClient.Delete(suite.ctx, ns)).To(Succeed())
	}

	g := NewWithT(t)
	namespace := setup(t, g)
	defer teardown(t, g, namespace)

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: namespace.Name})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneEndpoint: clusterv1.APIEndpoint{
			Host: "test.local",
			Port: 9999,
		},
	}
	g.Expect(fakeClient.Create(suite.ctx, cluster)).To(Succeed())
	patchHelper, err := patch.NewHelper(cluster, fakeClient)
	g.Expect(err).To(BeNil())
	cluster.Status = clusterv1.ClusterStatus{InfrastructureReady: true}
	g.Expect(patchHelper.Patch(suite.ctx, cluster)).To(Succeed())

	genericInfrastructureMachineTemplate := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind":       "GenericInfrastructureMachineTemplate",
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
			"metadata": map[string]interface{}{
				"name":      "infra-foo",
				"namespace": cluster.Namespace,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"hello": "world",
					},
				},
			},
		},
	}
	g.Expect(fakeClient.Create(suite.ctx, genericInfrastructureMachineTemplate)).To(Succeed())

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      "foo",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       cluster.Name,
					UID:        cluster.UID,
				},
			},
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Replicas: nil,
			Version:  "v1.16.6",
			InfrastructureTemplate: corev1.ObjectReference{
				Kind:       genericInfrastructureMachineTemplate.GetKind(),
				APIVersion: genericInfrastructureMachineTemplate.GetAPIVersion(),
				Name:       genericInfrastructureMachineTemplate.GetName(),
				Namespace:  cluster.Namespace,
			},
			ControlPlaneConfig: controlplanev1.ControlPlaneConfig{},
			RolloutStrategy: &controlplanev1.RolloutStrategy{
				Type: "RollingUpdate",
				RollingUpdate: &controlplanev1.RollingUpdate{
					MaxSurge: &intstr.IntOrString{
						IntVal: 1,
					},
				},
			},
		},
	}
	g.Expect(fakeClient.Create(suite.ctx, tcp)).To(Succeed())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	result, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	// this first requeue is to add finalizer
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(r.APIReader.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	m, n := createMachineNodePair("foo-machine1", cluster, tcp, true, suite.machineAddress)
	g.Expect(fakeClient.Create(suite.ctx, m)).To(Succeed())
	g.Expect(fakeClient.Create(suite.ctx, n)).To(Succeed())

	g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, suite.machineAddress))

	// fake etcd service state running
	suite.machineService.setServiceListResponse(&machine.ServiceListResponse{
		Messages: []*machine.ServiceList{
			{
				Metadata: &common.Metadata{
					Hostname: "foo-machine1",
				},
				Services: []*machine.ServiceInfo{
					{
						Id:    "etcd",
						State: "running",
						Events: &machine.ServiceEvents{
							Events: []*machine.ServiceEvent{
								{
									Msg:   "running",
									State: "Running",
								},
							},
						},
						Health: &machine.ServiceHealth{
							Healthy: true,
						},
					},
				},
			},
		},
	})

	// fake etcd members list
	suite.machineService.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
		Messages: []*machine.EtcdMembers{
			{
				Metadata: &common.Metadata{
					Hostname: "foo-machine1",
				},
				Members: []*machine.EtcdMember{
					{
						Id:       0,
						Hostname: "foo-machine1",
					},
				},
			},
		},
	})

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		// Expect the referenced infrastructure template to have a Cluster Owner Reference.
		g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(genericInfrastructureMachineTemplate), genericInfrastructureMachineTemplate)).To(Succeed())
		g.Expect(genericInfrastructureMachineTemplate.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
		}))

		// Always expect that the Finalizer is set on the passed in resource
		g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

		g.Expect(tcp.Status.Selector).NotTo(BeEmpty())
		g.Expect(tcp.Status.Replicas).To(BeEquivalentTo(1))
		g.Expect(conditions.IsTrue(tcp, controlplanev1.AvailableCondition)).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(1))

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(1))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))
	}, time.Second).Should(Succeed())
}

func TestSuite(t *testing.T) {
	suite.Run(t, &ControllersSuite{})
}
