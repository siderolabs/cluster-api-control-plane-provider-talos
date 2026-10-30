// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	bootstrapv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/stretchr/testify/suite"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/controllers"
)

type ControllersSuite struct {
	suite.Suite

	machineServices map[string]*machineService
	secretsBundle   *secrets.Bundle
	ctx             context.Context
	cancel          context.CancelFunc
}

func (suite *ControllersSuite) SetupSuite() {
	var err error

	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), time.Minute*10)

	suite.secretsBundle, err = secrets.NewBundle(secrets.NewFixedClock(time.Now()), config.TalosVersionCurrent)
	suite.Require().NoError(err)

	suite.machineServices = map[string]*machineService{}
}

func (suite *ControllersSuite) TearDownSuite() {
	suite.cancel()
}

func (suite *ControllersSuite) startMachineServer() (*machineService, string) {
	machineService, machineAddress, err := startMachineServer(suite.ctx, suite.secretsBundle)
	suite.Require().NoError(err)

	suite.machineServices[machineAddress] = machineService

	return machineService, machineAddress
}

func (suite *ControllersSuite) TestClusterToTalosControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneRef: clusterv1.ContractVersionedObjectReference{
			Kind:     "TalosControlPlane",
			Name:     "tcp-foo",
			APIGroup: controlplanev1.GroupVersion.Group,
		},
	}

	expectedResult := []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{Name: cluster.Spec.ControlPlaneRef.Name, Namespace: metav1.NamespaceDefault},
		},
	}

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	got := r.ClusterToTalosControlPlane(context.Background(), cluster)
	g.Expect(got).To(Equal(expectedResult))
}

func (suite *ControllersSuite) TestClusterToTalosControlPlaneNoControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	got := r.ClusterToTalosControlPlane(context.Background(), cluster)
	g.Expect(got).To(BeNil())
}

func (suite *ControllersSuite) TestClusterToTalosControlPlaneOtherControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneRef: clusterv1.ContractVersionedObjectReference{
			Kind:     "OtherControlPlane",
			Name:     "other-foo",
			APIGroup: controlplanev1.GroupVersion.Group,
		},
	}

	r := &controllers.TalosControlPlaneReconciler{
		Client:    fakeClient,
		APIReader: fakeClient,
	}

	got := r.ClusterToTalosControlPlane(context.Background(), cluster)
	g.Expect(got).To(BeNil())
}

func (suite *ControllersSuite) TestReconcilePaused() {
	g := NewWithT(suite.T())

	clusterName := "foo"

	// Test: cluster is paused and tcp is not
	cluster := newCluster(&types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: clusterName})

	cluster.Spec.Paused = pointer.Bool(true)

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
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: InfrastructureGroupVersion.Group,
						Kind:     "GenericInfrastructureMachineTemplate",
						Name:     "infra-foo",
					},
				},
			},
		},
	}
	tcp.Default(suite.T().Context(), tcp)
	_, err := tcp.ValidateCreate(suite.T().Context(), tcp)
	g.Expect(err).To(Succeed())
	fakeClient := newFakeClient(tcp.DeepCopy(), cluster.DeepCopy())
	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	machineList := &clusterv1.MachineList{}
	g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(metav1.NamespaceDefault))).To(Succeed())
	g.Expect(machineList.Items).To(BeEmpty())

	g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(BeEmpty(), "a paused TalosControlPlane must not be reconciled")
	expectCondition(g, tcp, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	g.Expect(conditions.Get(tcp, clusterv1.PausedCondition).Message).To(ContainSubstring("Cluster spec.paused is set to true"))

	// Test: tcp is paused and cluster is not
	g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(cluster), cluster)).To(Succeed())
	cluster.Spec.Paused = pointer.Bool(false)
	g.Expect(fakeClient.Update(suite.ctx, cluster)).To(Succeed())

	tcp.Annotations = map[string]string{clusterv1.PausedAnnotation: "paused"}
	g.Expect(fakeClient.Update(suite.ctx, tcp)).To(Succeed())

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(BeEmpty(), "a paused TalosControlPlane must not be reconciled")
	expectCondition(g, tcp, clusterv1.PausedCondition, metav1.ConditionTrue, clusterv1.PausedReason)
	g.Expect(conditions.Get(tcp, clusterv1.PausedCondition).Message).To(ContainSubstring("TalosControlPlane has the cluster.x-k8s.io/paused annotation"))

	// Test: neither is paused, reconciliation resumes
	delete(tcp.Annotations, clusterv1.PausedAnnotation)
	g.Expect(fakeClient.Update(suite.ctx, tcp)).To(Succeed())

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))
	expectCondition(g, tcp, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)
}

func (suite *ControllersSuite) TestReconcileClusterNoEndpoints() {
	g := NewWithT(suite.T())

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})
	cluster.Status = clusterv1.ClusterStatus{
		Initialization: clusterv1.ClusterInitializationStatus{
			InfrastructureProvisioned: pointer.Bool(true),
		},
		Conditions: []metav1.Condition{
			{
				Type:   string(clusterv1.InfrastructureReadyCondition),
				Status: metav1.ConditionTrue,
			},
		},
	}

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
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Kind:     "UnknownInfraMachine",
						APIGroup: "test",
						Name:     "foo",
					},
				},
			},
		},
	}

	tcp.Default(suite.T().Context(), tcp)
	_, err := tcp.ValidateCreate(suite.T().Context(), tcp)
	g.Expect(err).To(Succeed())

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

	// With no Machines the replica counters are set to an explicit 0 rather than left unset.
	g.Expect(tcp.Status.Replicas).To(BeZero())
	g.Expect(tcp.Status.ReadyReplicas).To(BeZero())
	g.Expect(tcp.Status.AvailableReplicas).To(HaveValue(BeZero()))
	g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeZero()))
	expectCondition(g, tcp, clusterv1.AvailableCondition, metav1.ConditionFalse, controlplanev1.NotAvailableReason)

	_, err = secret.GetFromNamespacedName(suite.ctx, fakeClient, client.ObjectKey{Namespace: metav1.NamespaceDefault, Name: "foo"}, secret.ClusterCA)
	g.Expect(err).NotTo(HaveOccurred())

	machineList := &clusterv1.MachineList{}
	g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(metav1.NamespaceDefault))).To(Succeed())
	g.Expect(machineList.Items).To(BeEmpty())
}

func (suite *ControllersSuite) TestReconcileInitializeControlPlane() {
	fakeClient := newFakeClient()

	cluster, tcp, infrastructureMachineTemplate := suite.setupCluster(fakeClient, "test-reconcile-init", nil)

	g := NewWithT(suite.T())

	// The Machine below is created without minReadySeconds, so it has to be synced in place.
	tcp.Spec.MinReadySeconds = pointer.Int32(15)
	g.Expect(fakeClient.Update(suite.ctx, tcp)).To(Succeed())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	result, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	// this first requeue is to add finalizer
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(r.APIReader.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	machineService, machineAddress := suite.startMachineServer()

	m, n := createMachineNodePair("foo-machine1", cluster, tcp, true, machineAddress)
	g.Expect(fakeClient.Create(suite.ctx, m)).To(Succeed())
	g.Expect(fakeClient.Create(suite.ctx, n)).To(Succeed())

	g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, machineAddress))

	// fake etcd service state running
	machineService.setServiceListResponse(&machine.ServiceListResponse{
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
	machineService.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
		Messages: []*machine.EtcdMembers{
			{
				Metadata: &common.Metadata{
					Hostname: "foo-machine1",
				},
				Members: []*machine.EtcdMember{
					{
						Id:       1,
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
		g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(infrastructureMachineTemplate), infrastructureMachineTemplate)).To(Succeed())
		g.Expect(infrastructureMachineTemplate.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
		}))

		// Always expect that the Finalizer is set on the passed in resource
		g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

		g.Expect(tcp.Status.Selector).NotTo(BeEmpty())
		g.Expect(tcp.Status.Replicas).To(BeEquivalentTo(1))
		g.Expect(conditions.IsTrue(tcp, string(controlplanev1.AvailableCondition))).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(1))

		// v1beta2 contract fields read by Cluster API.
		g.Expect(tcp.V1Beta1DeprecatedStatus().Initialized).To(BeTrue())
		g.Expect(tcp.Status.Initialization.ControlPlaneInitialized).To(HaveValue(BeTrue()))
		g.Expect(tcp.Status.AvailableReplicas).To(HaveValue(BeEquivalentTo(1)))
		g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeEquivalentTo(1)))
		expectCondition(g, tcp, clusterv1.AvailableCondition, metav1.ConditionTrue, controlplanev1.AvailableReason)
		expectCondition(g, tcp, clusterv1.MachinesReadyCondition, metav1.ConditionTrue, controlplanev1.MachinesReadyReason)
		expectCondition(g, tcp, clusterv1.MachinesUpToDateCondition, metav1.ConditionTrue, controlplanev1.MachinesUpToDateReason)
		expectCondition(g, tcp, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(1))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))
		g.Expect(machine.Spec.MinReadySeconds).To(HaveValue(BeEquivalentTo(15)))
		expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionTrue, clusterv1.MachineUpToDateReason)
	}, time.Second).Should(Succeed())

	// A reconcile fetches the infrastructure machine of each Machine once, for the rollout and the status alike.
	infraGets := map[string]int{}

	countingClient := interceptor.NewClient(fakeClient.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if obj.GetObjectKind().GroupVersionKind().Kind == GenericInfrastructureMachineCRD.Spec.Names.Kind {
				infraGets[key.Name]++
			}

			return c.Get(ctx, key, obj, opts...)
		},
	})

	r = newReconciler(countingClient, withCluster(util.ObjectKey(cluster)))

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(infraGets).To(Equal(map[string]int{"foo-machine1-infra": 1}))
}

func (suite *ControllersSuite) TestRollingUpdate() {
	fakeClient := newFakeClient()

	cluster, tcp, infrastructureMachineTemplate := suite.setupCluster(fakeClient, "test-rolling-update", pointer.Int32(2))

	g := NewWithT(suite.T())

	// New Machines are created with minReadySeconds from the TalosControlPlane.
	tcp.Spec.MinReadySeconds = pointer.Int32(10)
	g.Expect(fakeClient.Update(suite.ctx, tcp)).To(Succeed())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	result, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	// this first requeue is to add finalizer
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(r.APIReader.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	var eg errgroup.Group

	ctx, cancel := context.WithTimeout(suite.ctx, time.Minute)
	defer cancel()

	// background loop that maintains the state of Talos API responses, adds node refs and addresses to the machine, creates node response
	// it basically adjusts the state to be consistent for a running and healthy cluster
	eg.Go(func() error {
		suite.runUpdater(ctx, fakeClient, cluster)

		return nil
	})

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		// Expect the referenced infrastructure template to have a Cluster Owner Reference.
		g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(infrastructureMachineTemplate), infrastructureMachineTemplate)).To(Succeed())
		g.Expect(infrastructureMachineTemplate.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
		}))

		// Always expect that the Finalizer is set on the passed in resource
		g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

		g.Expect(tcp.Status.Selector).NotTo(BeEmpty())
		g.Expect(tcp.Status.Replicas).To(BeEquivalentTo(2))
		g.Expect(conditions.IsTrue(tcp, string(controlplanev1.AvailableCondition))).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))

		// The controller initializes the control plane by itself, nothing is seeded by the test.
		g.Expect(tcp.V1Beta1DeprecatedStatus().Initialized).To(BeTrue())
		g.Expect(tcp.Status.Initialization.ControlPlaneInitialized).To(HaveValue(BeTrue()))
		g.Expect(tcp.Status.AvailableReplicas).To(HaveValue(BeEquivalentTo(2)))
		g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeEquivalentTo(2)))
		expectCondition(g, tcp, clusterv1.AvailableCondition, metav1.ConditionTrue, controlplanev1.AvailableReason)

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(2))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))

		for _, machine := range machineList.Items {
			g.Expect(machine.Spec.MinReadySeconds).To(HaveValue(BeEquivalentTo(10)))
			expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionTrue, clusterv1.MachineUpToDateReason)
		}
	}, time.Second).Should(Succeed())

	// Changing minReadySeconds updates the Machines in place, without a rollout.
	machinesBefore := machineNames(suite.getMachines(fakeClient, cluster))

	patchHelper, err := patch.NewHelper(tcp, fakeClient)
	g.Expect(err).To(BeNil())

	tcp.Spec.MinReadySeconds = pointer.Int32(20)
	g.Expect(patchHelper.Patch(suite.ctx, tcp)).To(Succeed())

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	machines := suite.getMachines(fakeClient, cluster)
	g.Expect(machineNames(machines)).To(ConsistOf(machinesBefore))

	for _, machine := range machines {
		g.Expect(machine.Spec.MinReadySeconds).To(HaveValue(BeEquivalentTo(20)))
		expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionTrue, clusterv1.MachineUpToDateReason)
	}

	g.Expect(r.APIReader.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())

	patchHelper, err = patch.NewHelper(tcp, fakeClient)
	tcp.Spec.Version = "v1.25.4"

	g.Expect(err).To(BeNil())
	g.Expect(patchHelper.Patch(suite.ctx, tcp)).To(Succeed())

	// The Machines are reported as not up-to-date as soon as the spec changes.
	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	suite.expectMachinesNotUpToDate(g, fakeClient, cluster, machinesBefore)

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))
		g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeEquivalentTo(2)))

		machines := suite.getMachines(fakeClient, cluster)
		g.Expect(machines).To(HaveLen(2))

		for _, machine := range machines {
			g.Expect(machine.Name).NotTo(BeElementOf(machinesBefore))
			g.Expect(machine.Spec.Version).ToNot(BeNil())
			g.Expect(machine.Spec.Version).To(BeEquivalentTo(tcp.Spec.Version))
			expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionTrue, clusterv1.MachineUpToDateReason)
		}
	}, time.Minute).Should(Succeed())

	// Drift the TalosConfig of every Machine away from the TalosControlPlane spec.
	machinesBefore = machineNames(suite.getMachines(fakeClient, cluster))

	for _, machine := range suite.getMachines(fakeClient, cluster) {
		talosconfig := &bootstrapv1alpha3.TalosConfig{}

		g.Expect(fakeClient.Get(suite.ctx, client.ObjectKey{Name: machine.Spec.Bootstrap.ConfigRef.Name, Namespace: cluster.Namespace}, talosconfig)).NotTo(HaveOccurred())

		patchHelper, err := patch.NewHelper(talosconfig, fakeClient)
		talosconfig.Spec.TalosVersion = "v1.5.0"

		g.Expect(err).To(BeNil())
		g.Expect(patchHelper.Patch(suite.ctx, talosconfig)).To(Succeed())
	}

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	suite.expectMachinesNotUpToDate(g, fakeClient, cluster, machinesBefore)

	// Wait until the drifted Machines are actually replaced by Machines whose TalosConfig matches the spec.
	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))
		g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeEquivalentTo(2)))
		expectCondition(g, tcp, string(controlplanev1.MachinesSpecUpToDateCondition), metav1.ConditionTrue, controlplanev1.MachinesSpecUpToDateReason)
		expectCondition(g, tcp, clusterv1.MachinesUpToDateCondition, metav1.ConditionTrue, controlplanev1.MachinesUpToDateReason)

		machines := suite.getMachines(fakeClient, cluster)
		g.Expect(machines).To(HaveLen(int(tcp.Spec.GetReplicas())))

		for _, machine := range machines {
			g.Expect(machine.Name).NotTo(BeElementOf(machinesBefore))
			g.Expect(machine.Spec.Version).To(BeEquivalentTo(tcp.Spec.Version))

			talosconfig := &bootstrapv1alpha3.TalosConfig{}
			g.Expect(fakeClient.Get(suite.ctx, client.ObjectKey{Name: machine.Spec.Bootstrap.ConfigRef.Name, Namespace: cluster.Namespace}, talosconfig)).To(Succeed())
			g.Expect(talosconfig.Spec).To(Equal(tcp.Spec.ControlPlaneConfig.ControlPlaneConfig))

			expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionTrue, clusterv1.MachineUpToDateReason)
		}
	}, time.Minute).Should(Succeed())

	cancel()
	g.Expect(eg.Wait()).To(Succeed())
}

// expectMachinesNotUpToDate checks that the named Machines still exist and report UpToDate False.
func (suite *ControllersSuite) expectMachinesNotUpToDate(g *WithT, fakeClient client.Client, cluster *clusterv1.Cluster, names []string) {
	found := 0

	for _, machine := range suite.getMachines(fakeClient, cluster) {
		if !slices.Contains(names, machine.Name) {
			continue
		}

		found++

		expectCondition(g, &machine, clusterv1.MachineUpToDateCondition, metav1.ConditionFalse, clusterv1.MachineNotUpToDateReason)
	}

	g.Expect(found).To(Equal(len(names)), "the outdated machines must not be deleted before they are reported as not up-to-date")
}

func (suite *ControllersSuite) TestUppercaseHostnames() {
	fakeClient := newFakeClient()

	cluster, tcp, infrastructureMachineTemplate := suite.setupCluster(fakeClient, "test-uppercase-hostnames", pointer.Int32(3))

	g := NewWithT(suite.T())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	result, err := r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())
	// this first requeue is to add finalizer
	g.Expect(result).To(Equal(ctrl.Result{}))
	g.Expect(r.APIReader.Get(suite.ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
	g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

	var eg errgroup.Group

	ctx, cancel := context.WithTimeout(suite.ctx, time.Minute)
	defer cancel()

	// background loop that maintains the state of Talos API responses, adds node refs and addresses to the machine, creates node response
	// it basically adjusts the state to be consistent for a running and healthy cluster
	eg.Go(func() error {
		suite.runUpdater(ctx, fakeClient, cluster)

		return nil
	})

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		// Expect the referenced infrastructure template to have a Cluster Owner Reference.
		g.Expect(fakeClient.Get(suite.ctx, util.ObjectKey(infrastructureMachineTemplate), infrastructureMachineTemplate)).To(Succeed())
		g.Expect(infrastructureMachineTemplate.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Cluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
		}))

		// Always expect that the Finalizer is set on the passed in resource
		g.Expect(tcp.Finalizers).To(ContainElement(controlplanev1.TalosControlPlaneFinalizer))

		g.Expect(tcp.Status.Selector).NotTo(BeEmpty())
		g.Expect(tcp.Status.Replicas).To(BeEquivalentTo(3))
		g.Expect(conditions.IsTrue(tcp, string(controlplanev1.AvailableCondition))).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(3))

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(3))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))
	}, time.Second).Should(Succeed())

	cancel()
	g.Expect(eg.Wait()).To(Succeed())

	time.Sleep(time.Second * 1)

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})

	removedMachines := map[uint64]any{}

	for _, m := range suite.getMachines(fakeClient, cluster) {
		ms, ok := suite.machineServices[m.Status.Addresses[0].Address]
		g.Expect(ok).To(BeTrue())

		for k := range ms.getEtcdRemoveMemberRequests() {
			removedMachines[k] = struct{}{}
		}
	}

	g.Expect(len(removedMachines)).To(BeEquivalentTo(0), "unexpected etcd remove member requests count was called")
}

func (suite *ControllersSuite) runUpdater(ctx context.Context, fakeClient client.Client, cluster *clusterv1.Cluster) {
	g := NewWithT(suite.T())

	ticker := time.NewTicker(time.Millisecond * 100)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			machines := suite.getMachines(fakeClient, cluster)
			for _, machine := range machines {
				if len(machine.Status.Addresses) == 0 {
					_, address := suite.startMachineServer()

					patchHelper, err := patch.NewHelper(&machine, fakeClient)
					machine.Status.Addresses = []clusterv1.MachineAddress{
						{
							Address: address,
							Type:    clusterv1.MachineInternalIP,
						},
					}
					machine.Status.NodeRef = clusterv1.MachineNodeReference{
						Name: machine.Name,
					}
					setMachineReadyAndAvailable(&machine)

					g.Expect(err).To(BeNil())
					g.Expect(patchHelper.Patch(suite.ctx, &machine)).To(Succeed())

					g.Expect(fakeClient.Create(ctx, createNode(&machine, true))).To(Succeed())

					g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, address))
				}

				address := machine.Status.Addresses[0].Address

				suite.setEtcdRunning(address)
				suite.updateEtcdMembers(fakeClient, cluster)
				suite.addBootEvents(fakeClient, cluster)
			}
		}
	}
}

func (suite *ControllersSuite) getMachines(fakeClient client.Client, cluster *clusterv1.Cluster) []clusterv1.Machine {
	g := NewWithT(suite.T())
	machineList := &clusterv1.MachineList{}
	g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())

	return machineList.Items
}

func (suite *ControllersSuite) updateEtcdMembers(fakeClient client.Client, cluster *clusterv1.Cluster) {
	g := NewWithT(suite.T())

	machines := suite.getMachines(fakeClient, cluster)

	members := make([]*machine.EtcdMember, 0, len(machines))

	for id, m := range machines {
		members = append(members, &machine.EtcdMember{
			Id:       uint64(id),
			Hostname: strings.ToUpper(m.Name),
		})
	}

	for _, m := range machines {
		if len(m.Status.Addresses) == 0 {
			continue
		}

		ms, ok := suite.machineServices[m.Status.Addresses[0].Address]
		g.Expect(ok).To(BeTrue())

		ms.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
			Messages: []*machine.EtcdMembers{
				{
					Metadata: &common.Metadata{
						Hostname: m.Name,
					},
					Members: members,
				},
			},
		})
	}
}

func (suite *ControllersSuite) addBootEvents(fakeClient client.Client, cluster *clusterv1.Cluster) {
	g := NewWithT(suite.T())

	machines := suite.getMachines(fakeClient, cluster)

	bootDoneEvents := map[string][]*machine.SequenceEvent{}

	for _, m := range machines {
		bootDoneEvents[m.Name] = []*machine.SequenceEvent{
			{
				Sequence: "boot",
				Action:   machine.SequenceEvent_START,
			}, {
				Sequence: "boot",
				Action:   machine.SequenceEvent_STOP,
			},
		}
	}

	for _, m := range machines {
		if len(m.Status.Addresses) == 0 {
			continue
		}

		ms, ok := suite.machineServices[m.Status.Addresses[0].Address]
		g.Expect(ok).To(BeTrue())

		ms.resetSequenceEvents()

		for name, events := range bootDoneEvents {
			g.Expect(ms.addSequenceEvents(name, events...)).To(Succeed())
		}
	}
}

func (suite *ControllersSuite) setEtcdRunning(address string) {
	g := NewWithT(suite.T())

	ms, ok := suite.machineServices[address]
	g.Expect(ok).To(BeTrue())

	ms.setServiceListResponse(
		&machine.ServiceListResponse{
			Messages: []*machine.ServiceList{
				{
					Metadata: &common.Metadata{
						Hostname: address,
					},
					Services: []*machine.ServiceInfo{
						{
							Id:    "etcd",
							State: "Running",
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
}

func (suite *ControllersSuite) setupCluster(fakeClient client.Client, ns string, replicas *int32) (*clusterv1.Cluster, *controlplanev1.TalosControlPlane, *unstructured.Unstructured) {
	t := suite.T()

	setup := func(t *testing.T, g *WithT) *corev1.Namespace {
		t.Helper()

		t.Log("Creating the namespace")

		ns := &corev1.Namespace{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1.SchemeGroupVersion.Version,
				Kind:       "Namespace",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		}

		g.Expect(fakeClient.Create(suite.ctx, ns)).To(Succeed())

		return ns
	}

	g := NewWithT(t)
	namespace := setup(t, g)

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

	cluster.Status = clusterv1.ClusterStatus{
		Initialization: clusterv1.ClusterInitializationStatus{
			InfrastructureProvisioned: pointer.Bool(true),
		},
		Conditions: []metav1.Condition{
			{
				Type:   string(clusterv1.InfrastructureReadyCondition), // User-friendly condition type that is consistent across CAPI versions
				Status: metav1.ConditionTrue,
				// Reason and LastTransitionTime are optional, but can be helpful for debugging and ensuring the condition is properly set
				Reason:             "InfrastructureReady",
				LastTransitionTime: metav1.Now(),
			},
		},
	}
	g.Expect(patchHelper.Patch(suite.ctx, cluster)).To(Succeed())

	t.Log("Creating the dummy CRD for GenericInfrastructureMachine")
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "genericinfrastructuremachines.infrastructure.cluster.x-k8s.io",
			Labels: map[string]string{
				// Ensure the CRD is recognized as a Cluster API infrastructure provider CRD by including the expected labels for both v1beta1 and v1beta2 versions of CAPI
				"cluster.x-k8s.io/v1beta2": "v1beta2",
				"cluster.x-k8s.io/v1beta1": "v1beta2",
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "infrastructure.cluster.x-k8s.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "GenericInfrastructureMachine",
				Plural:   "genericinfrastructuremachines",
				Singular: "genericinfrastructuremachine",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta2", // Should match the API version used in the test
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:                   "object",
							XPreserveUnknownFields: pointer.Bool(true), // Lets the controller handle arbitrary fields in the spec without needing to define a strict schema, which is useful for testing
						},
					},
				},
			},
		},
	}
	g.Expect(fakeClient.Create(suite.ctx, crd)).To(Succeed())

	t.Log("Creating the dummy CRD for GenericInfrastructureMachineTemplate")
	g.Expect(fakeClient.Create(suite.ctx, untypedCRD(InfrastructureGroupVersion.WithKind("GenericInfrastructureMachineTemplate")))).To(Succeed())

	genericInfrastructureMachineTemplate := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind":       "GenericInfrastructureMachineTemplate",
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta2",
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
			Replicas: replicas,
			Version:  "v1.16.6",
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Kind:     genericInfrastructureMachineTemplate.GetKind(),
						APIGroup: genericInfrastructureMachineTemplate.GroupVersionKind().Group,
						Name:     genericInfrastructureMachineTemplate.GetName(),
					},
				},
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

	return cluster, tcp, genericInfrastructureMachineTemplate
}

func TestSuite(t *testing.T) {
	suite.Run(t, &ControllersSuite{})
}
