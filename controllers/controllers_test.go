// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"context"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
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

	got := r.ClusterToTalosControlPlane(context.Background(), cluster)
	g.Expect(got).To(Equal(expectedResult))
}

func (suite *ControllersSuite) TestClusterToTalosControlPlaneNoControlPlane() {
	g := NewWithT(suite.T())
	fakeClient := newFakeClient()

	r := newReconciler(fakeClient)

	cluster := newCluster(&types.NamespacedName{Name: "foo", Namespace: metav1.NamespaceDefault})

	got := r.ClusterToTalosControlPlane(context.Background(), cluster)
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
	_, err := tcp.ValidateCreate()
	g.Expect(err).To(Succeed())
	fakeClient := newFakeClient(tcp.DeepCopy(), cluster.DeepCopy())
	r := newReconciler(fakeClient)

	_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
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
	_, err := tcp.ValidateCreate()
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
		g.Expect(conditions.IsTrue(tcp, controlplanev1.AvailableCondition)).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(1))

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(1))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))
	}, time.Second).Should(Succeed())
}

func (suite *ControllersSuite) TestRollingUpdate() {
	fakeClient := newFakeClient()

	cluster, tcp, infrastructureMachineTemplate := suite.setupCluster(fakeClient, "test-rolling-update", pointer.Int32(2))

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
		g.Expect(tcp.Status.Replicas).To(BeEquivalentTo(2))
		g.Expect(conditions.IsTrue(tcp, controlplanev1.AvailableCondition)).To(BeTrue())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))

		machineList := &clusterv1.MachineList{}
		g.Expect(fakeClient.List(suite.ctx, machineList, client.InNamespace(cluster.Namespace))).To(Succeed())
		g.Expect(machineList.Items).To(HaveLen(2))

		machine := machineList.Items[0]
		g.Expect(machine.Name).To(HavePrefix(tcp.Name))
	}, time.Second).Should(Succeed())

	patchHelper, err := patch.NewHelper(tcp, fakeClient)
	tcp.Spec.Version = "v1.25.4"

	g.Expect(err).To(BeNil())
	g.Expect(patchHelper.Patch(suite.ctx, tcp)).To(Succeed())

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))

		machines := suite.getMachines(fakeClient, cluster)
		for _, machine := range machines {
			g.Expect(machine.Spec.Version).ToNot(BeNil())
			g.Expect(*machine.Spec.Version).To(BeEquivalentTo(tcp.Spec.Version))
		}
	}, time.Minute).Should(Succeed())

	for _, machine := range suite.getMachines(fakeClient, cluster) {
		talosconfig := &bootstrapv1alpha3.TalosConfig{}

		g.Expect(fakeClient.Get(suite.ctx, client.ObjectKey{Name: machine.Spec.Bootstrap.ConfigRef.Name, Namespace: machine.Spec.Bootstrap.ConfigRef.Namespace}, talosconfig)).NotTo(HaveOccurred())

		patchHelper, err := patch.NewHelper(talosconfig, fakeClient)
		talosconfig.Spec.TalosVersion = "v1.5.0"

		g.Expect(err).To(BeNil())
		g.Expect(patchHelper.Patch(suite.ctx, talosconfig)).To(Succeed())
	}

	g.Eventually(func(g Gomega) {
		_, err = r.Reconcile(suite.ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(r.APIReader.Get(suite.ctx, client.ObjectKey{Name: tcp.Name, Namespace: tcp.Namespace}, tcp)).To(Succeed())
		g.Expect(tcp.Status.ReadyReplicas).To(BeEquivalentTo(2))

		machines := suite.getMachines(fakeClient, cluster)
		for _, machine := range machines {
			g.Expect(machine.Spec.Version).ToNot(BeNil())
			g.Expect(*machine.Spec.Version).To(BeEquivalentTo(tcp.Spec.Version))
		}
	}, time.Minute).Should(Succeed())

	cancel()
	g.Expect(eg.Wait()).To(Succeed())
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
		g.Expect(conditions.IsTrue(tcp, controlplanev1.AvailableCondition)).To(BeTrue())
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
					machine.Status.NodeRef = &corev1.ObjectReference{
						Kind:       "Node",
						APIVersion: corev1.SchemeGroupVersion.String(),
						Name:       machine.Name,
					}

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
			Replicas: replicas,
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

	return cluster, tcp, genericInfrastructureMachineTemplate
}

func TestSuite(t *testing.T) {
	suite.Run(t, &ControllersSuite{})
}
