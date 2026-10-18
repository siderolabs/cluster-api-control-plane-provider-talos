// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const (
	remediationTestNamespace = "default"
	remediationTestCluster   = "test"
	remediationTestVersion   = "v1.34.0"
	infraMachineGroup        = "infrastructure.cluster.x-k8s.io"
	infraMachineKind         = "GenericInfrastructureMachine"

	// testFinalizer keeps deleted Machines around with a deletion timestamp, as the Machine controller does.
	testFinalizer = "test.cluster.x-k8s.io/keep"
)

// fakeEtcd is an etcdMemberManager that tracks calls instead of talking to Talos nodes.
type fakeEtcd struct {
	mu sync.Mutex

	client client.Client

	// unhealthy lists the machines whose etcd service is not healthy.
	unhealthy map[string]bool
	members   []*machineapi.EtcdMember
	listErr   error
	leaveErr  error
	removeErr error

	// healthDelay is how long each etcd service health check takes.
	healthDelay time.Duration

	calls []string
}

func (f *fakeEtcd) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)
}

func (f *fakeEtcd) getCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

func (f *fakeEtcd) ServiceHealth(_ context.Context, _ *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error {
	f.record("health:" + machine.Name)

	time.Sleep(f.healthDelay)

	if f.unhealthy[machine.Name] {
		return fmt.Errorf("etcd is not healthy on %s", machine.Name)
	}

	return nil
}

func (f *fakeEtcd) MemberList(_ context.Context, _ *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) ([]*machineapi.EtcdMember, error) {
	f.record("list:" + machine.Name)

	if f.listErr != nil {
		return nil, f.listErr
	}

	return f.members, nil
}

func (f *fakeEtcd) Leave(ctx context.Context, _ *controlplanev1.TalosControlPlane, machine *clusterv1.Machine) error {
	// Record whether the Machine was already deleted when it left etcd.
	current := &clusterv1.Machine{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(machine), current); err != nil {
		return err
	}

	f.record(fmt.Sprintf("leave:%s:deleting=%t:leaving=%s", machine.Name, !current.DeletionTimestamp.IsZero(), current.Annotations[etcdLeavingAnnotation]))

	return f.leaveErr
}

func (f *fakeEtcd) RemoveMember(_ context.Context, _ *controlplanev1.TalosControlPlane, via *clusterv1.Machine, member *machineapi.EtcdMember) error {
	f.record(fmt.Sprintf("remove:%s:via=%s", member.Hostname, via.Name))

	return f.removeErr
}

type machineOption func(*clusterv1.Machine)

func withUnhealthy() machineOption {
	return func(m *clusterv1.Machine) {
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.MachineHealthCheckSucceededCondition,
			Status: metav1.ConditionFalse,
			Reason: clusterv1.MachineHealthCheckNodeDeletedReason,
		})
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.MachineOwnerRemediatedCondition,
			Status: metav1.ConditionFalse,
			Reason: clusterv1.MachineOwnerRemediatedWaitingForRemediationReason,
		})
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.ReadyCondition,
			Status: metav1.ConditionFalse,
			Reason: clusterv1.NotReadyReason,
		})
	}
}

func withoutNodeRef() machineOption {
	return func(m *clusterv1.Machine) {
		m.Status.NodeRef = clusterv1.MachineNodeReference{}
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.ReadyCondition,
			Status: metav1.ConditionFalse,
			Reason: clusterv1.NotReadyReason,
		})
	}
}

func withNotReady() machineOption {
	return func(m *clusterv1.Machine) {
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.ReadyCondition,
			Status: metav1.ConditionFalse,
			Reason: clusterv1.NotReadyReason,
		})
	}
}

func withAnnotation(key, value string) machineOption {
	return func(m *clusterv1.Machine) {
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}

		m.Annotations[key] = value
	}
}

func newTestMachine(name string, created time.Time, opts ...machineOption) *clusterv1.Machine {
	m := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         remediationTestNamespace,
			CreationTimestamp: metav1.NewTime(created),
			Finalizers:        []string{testFinalizer},
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         remediationTestCluster,
				clusterv1.MachineControlPlaneLabel: "",
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: remediationTestCluster,
			Version:     remediationTestVersion,
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infraMachineGroup,
				Kind:     infraMachineKind,
				Name:     name,
			},
		},
		Status: clusterv1.MachineStatus{
			NodeRef: clusterv1.MachineNodeReference{Name: name},
			Conditions: []metav1.Condition{
				{
					Type:               clusterv1.ReadyCondition,
					Status:             metav1.ConditionTrue,
					Reason:             clusterv1.ReadyReason,
					LastTransitionTime: metav1.NewTime(created),
				},
				{
					Type:               clusterv1.MachineHealthCheckSucceededCondition,
					Status:             metav1.ConditionTrue,
					Reason:             clusterv1.MachineHealthCheckSucceededReason,
					LastTransitionTime: metav1.NewTime(created),
				},
			},
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

func newTestTCP(replicas int32) *controlplanev1.TalosControlPlane {
	return &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp",
			Namespace: remediationTestNamespace,
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Replicas: ptr.To(replicas),
			Version:  remediationTestVersion,
		},
		Status: controlplanev1.TalosControlPlaneStatus{
			Initialized: true,
			Initialization: controlplanev1.TalosControlPlaneInitializationStatus{
				ControlPlaneInitialized: ptr.To(true),
			},
		},
	}
}

func newTestCluster() *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      remediationTestCluster,
			Namespace: remediationTestNamespace,
		},
	}
}

// etcdMembersFor returns one healthy voting etcd member per machine name.
func etcdMembersFor(names ...string) []*machineapi.EtcdMember {
	members := make([]*machineapi.EtcdMember, 0, len(names))

	for i, name := range names {
		members = append(members, &machineapi.EtcdMember{Id: uint64(i + 1), Hostname: name})
	}

	return members
}

type remediationEnv struct {
	t       *testing.T
	ctx     context.Context
	client  client.Client
	etcd    *fakeEtcd
	r       *TalosControlPlaneReconciler
	tcp     *controlplanev1.TalosControlPlane
	cluster *clusterv1.Cluster
}

func newRemediationEnv(t *testing.T, tcp *controlplanev1.TalosControlPlane, machines ...*clusterv1.Machine) *remediationEnv {
	t.Helper()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clusterv1.AddToScheme,
		controlplanev1.AddToScheme,
		corev1.AddToScheme,
		apiextensionsv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	// The CRD of the infrastructure machines, so that their apiVersion can be resolved. The infrastructure machines
	// themselves don't exist, which the control plane tolerates.
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: contract.CalculateCRDName(infraMachineGroup, infraMachineKind),
			Labels: map[string]string{
				clusterv1.GroupVersion.Group + "/" + clusterv1.GroupVersion.Version: "v1beta2",
			},
		},
	}

	objs := []client.Object{crd, tcp}
	for _, m := range machines {
		objs = append(objs, m)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&clusterv1.Machine{}, &controlplanev1.TalosControlPlane{}).
		Build()

	etcd := &fakeEtcd{client: c, unhealthy: map[string]bool{}}

	return &remediationEnv{
		t:      t,
		ctx:    context.Background(),
		client: c,
		etcd:   etcd,
		r: &TalosControlPlaneReconciler{
			Client:    c,
			APIReader: c,
			Log:       testr.New(t),
			Scheme:    scheme,
			etcd:      etcd,
		},
		tcp:     tcp,
		cluster: newTestCluster(),
	}
}

func (e *remediationEnv) machines() []clusterv1.Machine {
	e.t.Helper()

	list := &clusterv1.MachineList{}
	if err := e.client.List(e.ctx, list, client.InNamespace(remediationTestNamespace)); err != nil {
		e.t.Fatal(err)
	}

	return list.Items
}

func (e *remediationEnv) machine(name string) *clusterv1.Machine {
	e.t.Helper()

	m := &clusterv1.Machine{}
	if err := e.client.Get(e.ctx, client.ObjectKey{Namespace: remediationTestNamespace, Name: name}, m); err != nil {
		e.t.Fatal(err)
	}

	return m
}

// storedTCP returns the TalosControlPlane as stored by the API server, without the changes that are only persisted
// by the deferred patch at the end of a reconcile.
func (e *remediationEnv) storedTCP() *controlplanev1.TalosControlPlane {
	e.t.Helper()

	tcp := &controlplanev1.TalosControlPlane{}
	if err := e.client.Get(e.ctx, client.ObjectKeyFromObject(e.tcp), tcp); err != nil {
		e.t.Fatal(err)
	}

	return tcp
}

func (e *remediationEnv) remediate() (bool, error) {
	_, remediated, err := e.r.reconcileUnhealthyMachines(e.ctx, e.tcp, e.cluster, e.machines(), nil)

	return remediated, err
}

// expectNotRemediated checks that the machine is not deleted and that its OwnerRemediated condition has the reason.
func (e *remediationEnv) expectNotRemediated(g *WithT, name, reason, messageSubstring string) {
	m := e.machine(name)
	g.Expect(m.DeletionTimestamp.IsZero()).To(BeTrue(), "machine %s should not be deleted", name)

	condition := conditions.Get(m, clusterv1.MachineOwnerRemediatedCondition)
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal(reason))
	g.Expect(condition.Message).To(ContainSubstring(messageSubstring))

	g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
}

func (e *remediationEnv) expectRemediated(g *WithT, name string) {
	m := e.machine(name)
	g.Expect(m.DeletionTimestamp.IsZero()).To(BeFalse(), "machine %s should be deleted", name)

	condition := conditions.Get(m, clusterv1.MachineOwnerRemediatedCondition)
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Reason).To(Equal(remediationMachineDeletingReason))

	g.Expect(e.tcp.Annotations).To(HaveKey(remediationInProgressAnnotation))

	data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(data.Machine).To(Equal(name))
}

func (e *remediationEnv) deletedMachines() []string {
	var names []string

	for _, m := range e.machines() {
		if !m.DeletionTimestamp.IsZero() {
			names = append(names, m.Name)
		}
	}

	return names
}

func TestRemediationCleansUpStaleOwnerRemediated(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	// m1 was marked for remediation, but is healthy again.
	recovered := newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy())
	conditions.Set(recovered, metav1.Condition{
		Type:   clusterv1.MachineHealthCheckSucceededCondition,
		Status: metav1.ConditionTrue,
		Reason: clusterv1.MachineHealthCheckSucceededReason,
	})

	e := newRemediationEnv(t, newTestTCP(3),
		recovered,
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeFalse())

	m1 := e.machine("m1")
	g.Expect(conditions.Has(m1, clusterv1.MachineOwnerRemediatedCondition)).To(BeFalse())
	g.Expect(m1.DeletionTimestamp.IsZero()).To(BeTrue())
	g.Expect(e.etcd.getCalls()).To(BeEmpty())
}

func TestRemediationPreflightChecks(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		name     string
		tcp      func() *controlplanev1.TalosControlPlane
		machines func() []*clusterv1.Machine
		reason   string
		message  string
	}{
		{
			name: "control plane not initialized",
			tcp: func() *controlplanev1.TalosControlPlane {
				tcp := newTestTCP(3)
				tcp.Status.Initialized = false
				tcp.Status.Initialization.ControlPlaneInitialized = nil

				return tcp
			},
			machines: func() []*clusterv1.Machine {
				return []*clusterv1.Machine{
					newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
					newTestMachine("m2", now.Add(-2*time.Hour)),
					newTestMachine("m3", now.Add(-time.Hour)),
				}
			},
			reason:  remediationDeferredReason,
			message: "before the control plane is initialized",
		},
		{
			name: "another machine is still provisioning",
			machines: func() []*clusterv1.Machine {
				return []*clusterv1.Machine{
					newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
					newTestMachine("m2", now.Add(-2*time.Hour)),
					newTestMachine("m3", now.Add(-time.Hour), withoutNodeRef()),
				}
			},
			reason:  remediationDeferredReason,
			message: "provisioning of control plane machine m3",
		},
		{
			name: "another machine is leaving etcd",
			machines: func() []*clusterv1.Machine {
				return []*clusterv1.Machine{
					newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
					newTestMachine("m2", now.Add(-2*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
					newTestMachine("m3", now.Add(-time.Hour)),
				}
			},
			reason:  remediationDeferredReason,
			message: "deletion of control plane machine m2",
		},
		{
			name: "single machine",
			tcp: func() *controlplanev1.TalosControlPlane {
				return newTestTCP(1)
			},
			machines: func() []*clusterv1.Machine {
				return []*clusterv1.Machine{
					newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
				}
			},
			reason:  cannotBeRemediatedReason,
			message: "less or equal to 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			tcp := newTestTCP(3)
			if tc.tcp != nil {
				tcp = tc.tcp()
			}

			e := newRemediationEnv(t, tcp, tc.machines()...)
			e.etcd.members = etcdMembersFor("m1", "m2", "m3")

			remediated, err := e.remediate()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(remediated).To(BeFalse())

			e.expectNotRemediated(g, "m1", tc.reason, tc.message)
			g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
		})
	}
}

func TestRemediationDefersWhileAnotherMachineIsDeleting(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")

	g.Expect(e.client.Delete(e.ctx, e.machine("m2"))).To(Succeed())

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeFalse())

	e.expectNotRemediated(g, "m1", remediationDeferredReason, "deletion of control plane machine m2")
}

func TestRemediationRefusesToLoseEtcdQuorum(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		name      string
		replicas  int32
		machines  []string
		unhealthy []string
	}{
		{
			// Removing the member of the failed machine needs both members of a 2 member cluster.
			name:      "2 members, the remediated member is unhealthy",
			replicas:  2,
			machines:  []string{"m1", "m2"},
			unhealthy: []string{"m1"},
		},
		{
			name:      "2 members, the other member is unhealthy",
			replicas:  2,
			machines:  []string{"m1", "m2"},
			unhealthy: []string{"m2"},
		},
		{
			name:      "3 members, another member is unhealthy",
			replicas:  3,
			machines:  []string{"m1", "m2", "m3"},
			unhealthy: []string{"m1", "m3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			machines := make([]*clusterv1.Machine, 0, len(tc.machines))
			for i, name := range tc.machines {
				var opts []machineOption
				if i == 0 {
					opts = append(opts, withUnhealthy())
				}

				machines = append(machines, newTestMachine(name, now.Add(time.Duration(i-10)*time.Hour), opts...))
			}

			e := newRemediationEnv(t, newTestTCP(tc.replicas), machines...)
			e.etcd.members = etcdMembersFor(tc.machines...)

			for _, name := range tc.unhealthy {
				e.etcd.unhealthy[name] = true
			}

			remediated, err := e.remediate()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(remediated).To(BeFalse())

			e.expectNotRemediated(g, "m1", cannotBeRemediatedReason, "etcd quorum loss")
			g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
		})
	}
}

func TestRemediationLeavesEtcdBeforeDeleting(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeTrue())

	// The machine was marked as leaving etcd and left etcd before it was deleted.
	g.Expect(e.etcd.getCalls()).To(ContainElement("leave:m1:deleting=false:leaving=true"))
	g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("remove:")))

	e.expectRemediated(g, "m1")
	g.Expect(e.deletedMachines()).To(ConsistOf("m1"))
}

func TestRemediationRemovesUnreachableMemberThroughAnotherMachine(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true
	e.etcd.leaveErr = errors.New("connection refused")

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeTrue())

	g.Expect(e.etcd.getCalls()).To(ContainElement("remove:m1:via=m2"))

	e.expectRemediated(g, "m1")
}

func TestRemediationKeepsMachineWhenEtcdMemberCantBeRemoved(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true
	e.etcd.leaveErr = errors.New("connection refused")
	e.etcd.removeErr = errors.New("context deadline exceeded")

	remediated, err := e.remediate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(remediated).To(BeTrue(), "the rest of the reconcile must not delete machines")

	e.expectNotRemediated(g, "m1", remediationInternalErrorReason, "Failed to remove the machine from etcd")
	g.Expect(e.machine("m1").Annotations).NotTo(HaveKey(etcdLeavingAnnotation))

	// The remediation was recorded before the etcd member was removed, and is withdrawn since it did not happen.
	g.Expect(e.storedTCP().Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
}

func TestRemediationUsesObservedEtcdStatuses(t *testing.T) {
	now := time.Now()

	healthy := func(members ...string) etcdMachineStatus {
		return etcdMachineStatus{healthy: true, members: etcdMembersFor(members...)}
	}

	for _, tc := range []struct {
		name     string
		observed map[string]etcdMachineStatus
		check    func(g *WithT, e *remediationEnv, remediated bool)
	}{
		{
			name: "remediates when the other members were observed healthy",
			observed: map[string]etcdMachineStatus{
				"m1": {},
				"m2": healthy("m1", "m2", "m3"),
				"m3": healthy("m1", "m2", "m3"),
			},
			check: func(g *WithT, e *remediationEnv, remediated bool) {
				g.Expect(remediated).To(BeTrue())
				e.expectRemediated(g, "m1")
			},
		},
		{
			name: "refuses when another member was observed unhealthy",
			observed: map[string]etcdMachineStatus{
				"m1": {},
				"m2": healthy("m1", "m2", "m3"),
				"m3": {},
			},
			check: func(g *WithT, e *remediationEnv, remediated bool) {
				g.Expect(remediated).To(BeFalse())
				e.expectNotRemediated(g, "m1", cannotBeRemediatedReason, "etcd quorum loss")
			},
		},
		{
			name: "refuses when another machine was not observed",
			observed: map[string]etcdMachineStatus{
				"m1": {},
				"m2": healthy("m1", "m2", "m3"),
			},
			check: func(g *WithT, e *remediationEnv, remediated bool) {
				g.Expect(remediated).To(BeFalse())
				e.expectNotRemediated(g, "m1", cannotBeRemediatedReason, "etcd quorum loss")
			},
		},
		{
			name: "defers when the members could not be listed",
			observed: map[string]etcdMachineStatus{
				"m1": {},
				"m2": {healthy: true, listErr: errors.New("connection refused")},
				"m3": {healthy: true, listErr: errors.New("connection refused")},
			},
			check: func(g *WithT, e *remediationEnv, remediated bool) {
				g.Expect(remediated).To(BeFalse())
				e.expectNotRemediated(g, "m1", remediationDeferredReason, "Failed to check etcd cluster health")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			e := newRemediationEnv(t, newTestTCP(3),
				newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
				newTestMachine("m2", now.Add(-2*time.Hour)),
				newTestMachine("m3", now.Add(-time.Hour)),
			)
			// etcd itself would tell otherwise: only the observed statuses must be used.
			e.etcd.members = etcdMembersFor("m1", "m2", "m3")
			e.etcd.unhealthy = map[string]bool{"m1": true, "m2": true, "m3": true}

			_, remediated, err := e.r.reconcileUnhealthyMachines(e.ctx, e.tcp, e.cluster, e.machines(), tc.observed)
			g.Expect(err).NotTo(HaveOccurred())

			tc.check(g, e, remediated)

			g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("health:")))
			g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("list:")))
		})
	}
}

func TestRemediationInspectsEtcdConcurrently(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true
	e.etcd.healthDelay = time.Second

	start := time.Now()

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeTrue())

	// Inspected one after the other, the 3 machines would take 3 seconds.
	g.Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
	g.Expect(e.etcd.getCalls()).To(ContainElements("health:m1", "health:m2", "health:m3"))
}

func TestMachineUpToDateConditionsReuseControlPlane(t *testing.T) {
	now := time.Now()

	// failInfraGets makes any fetch of an infrastructure machine or TalosConfig fail the test.
	failInfraGets := func(t *testing.T, e *remediationEnv) {
		e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				switch obj.(type) {
				case *unstructured.Unstructured, *cabptv1.TalosConfig:
					t.Errorf("unexpected fetch of %T %s", obj, key)
				}

				return c.Get(ctx, key, obj, opts...)
			},
		})
	}

	t.Run("the ControlPlane of the reconcile is reused", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-2*time.Hour)),
			newTestMachine("m2", now.Add(-time.Hour)),
		)

		list := &clusterv1.MachineList{Items: e.machines()}

		controlPlane, err := newControlPlane(e.ctx, e.client, e.cluster, e.tcp, collections.FromMachineList(list))
		g.Expect(err).NotTo(HaveOccurred())

		// m3 was created after the ControlPlane was built, and m2 runs an older version.
		m3 := newTestMachine("m3", now)
		g.Expect(e.client.Create(e.ctx, m3)).To(Succeed())

		m2 := e.machine("m2")
		m2.Spec.Version = "v1.33.0"
		g.Expect(e.client.Update(e.ctx, m2)).To(Succeed())

		failInfraGets(t, e)

		machines := collections.FromMachineList(&clusterv1.MachineList{Items: e.machines()})
		g.Expect(e.r.reconcileMachineUpToDateConditions(e.ctx, e.cluster, e.tcp, machines, controlPlane)).To(Succeed())

		g.Expect(conditions.IsTrue(e.machine("m1"), clusterv1.MachineUpToDateCondition)).To(BeTrue())
		g.Expect(conditions.IsFalse(e.machine("m2"), clusterv1.MachineUpToDateCondition)).To(BeTrue())
		g.Expect(conditions.IsTrue(e.machine("m3"), clusterv1.MachineUpToDateCondition)).To(BeTrue())
	})

	t.Run("they are left as is while the TalosControlPlane is being deleted", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-time.Hour)),
		)

		e.tcp.DeletionTimestamp = ptr.To(metav1.Now())

		failInfraGets(t, e)

		g.Expect(e.r.updateStatus(e.ctx, e.tcp, e.cluster, nil)).To(Succeed())

		g.Expect(conditions.Has(e.machine("m1"), clusterv1.MachineUpToDateCondition)).To(BeFalse())
		g.Expect(e.tcp.Status.Replicas).To(HaveValue(BeEquivalentTo(1)))
	})
}

func TestRemediationIsRecordedBeforeTheMachineIsTouched(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true

	// recorded tells, for the first time each event happened, whether the remediation was recorded on the server.
	recorded := map[string]bool{}

	recordedOnServer := func(event string) {
		if _, seen := recorded[event]; !seen {
			_, recorded[event] = e.storedTCP().Annotations[remediationInProgressAnnotation]
		}
	}

	e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if m, ok := obj.(*clusterv1.Machine); ok && m.Annotations[etcdLeavingAnnotation] == "true" {
				recordedOnServer("mark-leaving")
			}

			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			recordedOnServer("delete")

			return c.Delete(ctx, obj, opts...)
		},
	})

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeTrue())

	g.Expect(recorded).To(Equal(map[string]bool{"mark-leaving": true, "delete": true}))

	e.expectRemediated(g, "m1")
	g.Expect(e.storedTCP().Annotations).To(HaveKeyWithValue(remediationInProgressAnnotation, e.tcp.Annotations[remediationInProgressAnnotation]))
}

func TestRemediationDoesNotStartWhenItCantBeRecorded(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.unhealthy["m1"] = true

	e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*controlplanev1.TalosControlPlane); ok {
				return errors.New("injected patch failure")
			}

			return c.Patch(ctx, obj, patch, opts...)
		},
	})

	remediated, err := e.remediate()
	g.Expect(err).To(HaveOccurred())
	g.Expect(remediated).To(BeTrue(), "the rest of the reconcile must not delete machines")

	e.expectNotRemediated(g, "m1", remediationInternalErrorReason, "Failed to record the remediation")
	g.Expect(e.machine("m1").Annotations).NotTo(HaveKey(etcdLeavingAnnotation))
	g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
	g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("remove:")))
}

func TestRemediationInProgress(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	inProgress := func(g *WithT, machine string, at time.Time) string {
		value, err := (&remediationData{Machine: machine, Timestamp: metav1.NewTime(at)}).marshal()
		g.Expect(err).NotTo(HaveOccurred())

		return value
	}

	t.Run("blocks a second remediation", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress(g, "m0", now.Add(-time.Minute))}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			// The replacement of m0 is not ready yet.
			newTestMachine("m3", now, withNotReady()),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(remediated).To(BeFalse())

		m1 := e.machine("m1")
		g.Expect(m1.DeletionTimestamp.IsZero()).To(BeTrue())
		g.Expect(conditions.Get(m1, clusterv1.MachineOwnerRemediatedCondition).Reason).To(Equal(remediationDeferredReason))
		g.Expect(conditions.Get(m1, clusterv1.MachineOwnerRemediatedCondition).Message).To(ContainSubstring("previous remediation"))
		g.Expect(e.tcp.Annotations).To(HaveKey(remediationInProgressAnnotation))
	})

	t.Run("completes when the replacement is ready", func(t *testing.T) {
		g := NewWithT(t)

		value := inProgress(g, "m0", now.Add(-10*time.Minute))

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-5*time.Minute)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		// The remediation data moved to the replacement, and the next remediation could proceed.
		g.Expect(e.machine("m3").Annotations).To(HaveKeyWithValue(remediationForAnnotation, value))
		g.Expect(remediated).To(BeTrue())
		e.expectRemediated(g, "m1")
	})

	t.Run("does not complete while the replacement is not ready", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress(g, "m0", now.Add(-10*time.Minute))}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour)),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-5*time.Minute), withNotReady()),
		)

		_, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.tcp.Annotations).To(HaveKey(remediationInProgressAnnotation))
		g.Expect(e.machine("m3").Annotations).NotTo(HaveKey(remediationForAnnotation))
	})

	t.Run("times out when the replacement never becomes ready", func(t *testing.T) {
		g := NewWithT(t)

		value := inProgress(g, "m0", now.Add(-remediationInProgressTimeout-time.Minute))

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour)),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-remediationInProgressTimeout), withNotReady()),
		)

		_, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
		g.Expect(e.machine("m3").Annotations).To(HaveKeyWithValue(remediationForAnnotation, value))
	})

	t.Run("completes when no replacement is needed", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(2)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress(g, "m0", now.Add(-10*time.Minute))}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour)),
			newTestMachine("m2", now.Add(-2*time.Hour)),
		)

		_, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
	})

	t.Run("is removed when the remediated machine was neither removed from etcd nor deleted", func(t *testing.T) {
		g := NewWithT(t)

		// The controller stopped after recording the remediation of m1, before removing m1 from etcd.
		recordedAt := now.Add(-remediationStartTimeout - time.Minute)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress(g, "m1", recordedAt)}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		// m1 is remediated again, as a new remediation.
		g.Expect(remediated).To(BeTrue())
		e.expectRemediated(g, "m1")

		data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(data.Timestamp.After(recordedAt)).To(BeTrue())
		g.Expect(data.RetryCount).To(BeZero())
	})

	t.Run("is kept right after it was recorded", func(t *testing.T) {
		g := NewWithT(t)

		// The cached Machines may not show yet that m1 is leaving etcd or being deleted.
		value := inProgress(g, "m1", now.Add(-10*time.Second))

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(remediated).To(BeFalse())

		g.Expect(e.tcp.Annotations).To(HaveKeyWithValue(remediationInProgressAnnotation, value))
		g.Expect(e.deletedMachines()).To(BeEmpty())
		g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
	})

	t.Run("is kept while the remediated machine is leaving etcd", func(t *testing.T) {
		g := NewWithT(t)

		// m1 left etcd, but deleting it failed.
		value := inProgress(g, "m1", now.Add(-10*time.Minute))

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy(), withAnnotation(etcdLeavingAnnotation, "true")),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(remediated).To(BeFalse())

		g.Expect(e.tcp.Annotations).To(HaveKeyWithValue(remediationInProgressAnnotation, value))
	})

	t.Run("an annotation set by an earlier version is converted while the remediated machine is deleting", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: "m0"}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m0", now.Add(-4*time.Hour)),
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		g.Expect(e.client.Delete(e.ctx, e.machine("m0"))).To(Succeed())

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(remediated).To(BeFalse())

		// The remediation of m0 still blocks the next one.
		m1 := e.machine("m1")
		g.Expect(m1.DeletionTimestamp.IsZero()).To(BeTrue())
		g.Expect(conditions.Get(m1, clusterv1.MachineOwnerRemediatedCondition).Message).To(ContainSubstring("previous remediation"))

		data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(data.Machine).To(Equal("m0"))
		g.Expect(data.Timestamp.Time).To(BeTemporally("~", e.machine("m0").DeletionTimestamp.Time, time.Second))
	})

	t.Run("an annotation set by an earlier version is converted until the replacement is ready", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: "m0"}

		// m0 is gone, and m3, its replacement, is not ready yet.
		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Minute), withNotReady()),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		before := time.Now()

		remediated, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(remediated).To(BeFalse())

		m1 := e.machine("m1")
		g.Expect(m1.DeletionTimestamp.IsZero()).To(BeTrue())
		g.Expect(conditions.Get(m1, clusterv1.MachineOwnerRemediatedCondition).Message).To(ContainSubstring("previous remediation"))

		data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(data.Machine).To(Equal("m0"))
		g.Expect(data.Timestamp.Time).To(BeTemporally(">=", before.Truncate(time.Second)))
	})

	t.Run("an annotation set by an earlier version completes when enough machines are ready", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: "m0"}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour)),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Minute)),
		)

		_, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
	})

	t.Run("an annotation set by an earlier version is converted even if the reconcile fails", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: "m0"}

		// m1 recovered, so its OwnerRemediated condition is cleaned up, which fails.
		m1 := newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy())
		conditions.Set(m1, metav1.Condition{
			Type:   clusterv1.MachineHealthCheckSucceededCondition,
			Status: metav1.ConditionTrue,
			Reason: clusterv1.MachineHealthCheckSucceededReason,
		})

		e := newRemediationEnv(t, tcp,
			m1,
			newTestMachine("m2", now.Add(-2*time.Hour)),
		)

		e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*clusterv1.Machine); ok {
					return errors.New("injected patch failure")
				}

				return c.Patch(ctx, obj, patch, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if _, ok := obj.(*clusterv1.Machine); ok {
					return errors.New("injected patch failure")
				}

				return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		})

		_, err := e.remediate()
		g.Expect(err).To(MatchError(ContainSubstring("injected patch failure")))

		// Machines created by the scale up in the same reconcile copy the converted annotation.
		data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(data.Machine).To(Equal("m0"))
	})

	t.Run("an invalid annotation is removed", func(t *testing.T) {
		g := NewWithT(t)

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: `{"machine": "m0"`}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-3*time.Hour)),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour), withNotReady()),
		)

		_, err := e.remediate()
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
	})
}

func TestRemediationRetryLimits(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	remediationFor := func(g *WithT, at time.Time, retryCount int) string {
		value, err := (&remediationData{Machine: "m0", Timestamp: metav1.NewTime(at), RetryCount: retryCount}).marshal()
		g.Expect(err).NotTo(HaveOccurred())

		return value
	}

	for _, tc := range []struct {
		name           string
		lastRemediated time.Duration
		retryCount     int
		reason         string
		message        string
		wantRetryCount int
	}{
		{
			name:           "waits for the retry period",
			lastRemediated: remediationRetryPeriod / 2,
			reason:         remediationDeferredReason,
			message:        "already failed in the latest",
		},
		{
			name:           "stops after the maximum number of retries",
			lastRemediated: 2 * remediationRetryPeriod,
			retryCount:     remediationMaxRetry,
			reason:         cannotBeRemediatedReason,
			message:        fmt.Sprintf("already failed %d times", remediationMaxRetry),
		},
		{
			name:           "counts a retry",
			lastRemediated: 2 * remediationRetryPeriod,
			retryCount:     1,
			wantRetryCount: 2,
		},
		{
			name:           "starts a new sequence after the min healthy period",
			lastRemediated: 2 * remediationMinHealthyPeriod,
			retryCount:     remediationMaxRetry,
			wantRetryCount: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			e := newRemediationEnv(t, newTestTCP(3),
				newTestMachine("m1", now.Add(-3*time.Hour)),
				newTestMachine("m2", now.Add(-2*time.Hour)),
				newTestMachine("m3", now.Add(-tc.lastRemediated),
					withUnhealthy(),
					withAnnotation(remediationForAnnotation, remediationFor(g, now.Add(-tc.lastRemediated), tc.retryCount)),
				),
			)
			e.etcd.members = etcdMembersFor("m1", "m2", "m3")
			e.etcd.unhealthy["m3"] = true

			remediated, err := e.remediate()
			g.Expect(err).NotTo(HaveOccurred())

			if tc.reason != "" {
				g.Expect(remediated).To(BeFalse())
				e.expectNotRemediated(g, "m3", tc.reason, tc.message)

				return
			}

			g.Expect(remediated).To(BeTrue())
			e.expectRemediated(g, "m3")

			data, err := remediationDataFromAnnotation(e.tcp.Annotations[remediationInProgressAnnotation])
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(data.RetryCount).To(Equal(tc.wantRetryCount))
		})
	}
}

func TestMachineToBeRemediated(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	oldest := newTestMachine("oldest", now.Add(-3*time.Hour), withUnhealthy())
	provisioning := newTestMachine("provisioning", now.Add(-time.Hour), withUnhealthy(), withoutNodeRef())
	annotated := newTestMachine("annotated", now, withUnhealthy(), withAnnotation(clusterv1.RemediateMachineAnnotation, ""))
	healthy := newTestMachine("healthy", now.Add(-4*time.Hour))

	g.Expect(machineToBeRemediated([]*clusterv1.Machine{healthy})).To(BeNil())
	g.Expect(machineToBeRemediated([]*clusterv1.Machine{healthy, oldest, provisioning, annotated}).Name).To(Equal("annotated"))
	g.Expect(machineToBeRemediated([]*clusterv1.Machine{healthy, oldest, provisioning}).Name).To(Equal("provisioning"))
	g.Expect(machineToBeRemediated([]*clusterv1.Machine{healthy, oldest}).Name).To(Equal("oldest"))
}

func TestCanSafelyRemoveEtcdMember(t *testing.T) {
	now := time.Now()

	for _, tc := range []struct {
		name      string
		machines  int
		members   []*machineapi.EtcdMember
		unhealthy []int
		safe      bool
	}{
		{name: "1 member", machines: 1, safe: false},
		{name: "2 members, all healthy", machines: 2, safe: true},
		{name: "2 members, target unhealthy", machines: 2, unhealthy: []int{0}, safe: false},
		{name: "2 members, other unhealthy", machines: 2, unhealthy: []int{1}, safe: false},
		{name: "3 members, target unhealthy", machines: 3, unhealthy: []int{0}, safe: true},
		{name: "3 members, target and another unhealthy", machines: 3, unhealthy: []int{0, 2}, safe: false},
		{name: "5 members, target and another unhealthy", machines: 5, unhealthy: []int{0, 4}, safe: true},
		{name: "5 members, target and 2 others unhealthy", machines: 5, unhealthy: []int{0, 3, 4}, safe: false},
		{
			name:     "3 members, one is a learner",
			machines: 3,
			members: []*machineapi.EtcdMember{
				{Id: 1, Hostname: "m0"},
				{Id: 2, Hostname: "m1"},
				{Id: 3, Hostname: "m2", IsLearner: true},
			},
			safe: false,
		},
		{
			name:     "3 members, the target member can't be identified",
			machines: 3,
			members:  etcdMembersFor("other", "m1", "m2"),
			safe:     true,
		},
		{
			name:     "2 members, the target member can't be identified",
			machines: 2,
			members:  etcdMembersFor("other", "m1"),
			safe:     false,
		},
		{
			name:     "3 members, a member without machine",
			machines: 2,
			members:  etcdMembersFor("m0", "m1", "orphan"),
			safe:     false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			machines := make([]*clusterv1.Machine, 0, tc.machines)
			names := make([]string, 0, tc.machines)
			healthy := map[string]bool{}

			for i := range tc.machines {
				name := fmt.Sprintf("m%d", i)
				names = append(names, name)
				machines = append(machines, newTestMachine(name, now.Add(time.Duration(i)*time.Minute)))
				healthy[name] = true
			}

			for _, i := range tc.unhealthy {
				healthy[names[i]] = false
			}

			members := tc.members
			if members == nil {
				members = etcdMembersFor(names...)
			}

			safe, details := canSafelyRemoveEtcdMember(members, machines[0], machines, healthy)
			g.Expect(safe).To(Equal(tc.safe), details)
		})
	}
}

func TestRemediationAndScaleDownInTheSameReconcile(t *testing.T) {
	now := time.Now()

	t.Run("no scale down in the reconcile that remediated a machine", func(t *testing.T) {
		g := NewWithT(t)

		// 4 machines for 3 replicas: without remediation, the control plane would scale down.
		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-4*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-3*time.Hour)),
			newTestMachine("m3", now.Add(-2*time.Hour)),
			newTestMachine("m4", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3", "m4")
		e.etcd.unhealthy["m1"] = true

		machines := &clusterv1.MachineList{Items: e.machines()}

		result, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, machines, nil, nil)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(result.RequeueAfter).NotTo(BeZero())

		e.expectRemediated(g, "m1")
		g.Expect(e.deletedMachines()).To(ConsistOf("m1"))

		// The scale down did not run at all.
		g.Expect(conditions.Has(e.tcp, string(controlplanev1.ResizedCondition))).To(BeFalse())
	})

	t.Run("scale down waits while a remediation is in progress", func(t *testing.T) {
		g := NewWithT(t)

		value, err := (&remediationData{Machine: "m0", Timestamp: metav1.NewTime(now.Add(-time.Minute))}).marshal()
		g.Expect(err).NotTo(HaveOccurred())

		tcp := newTestTCP(3)
		tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

		e := newRemediationEnv(t, tcp,
			newTestMachine("m1", now.Add(-4*time.Hour)),
			newTestMachine("m2", now.Add(-3*time.Hour)),
			// m3 is not ready and m4, the replacement, is not ready yet.
			newTestMachine("m3", now.Add(-2*time.Hour), withNotReady()),
			newTestMachine("m4", now, withNotReady()),
		)

		machines := &clusterv1.MachineList{Items: e.machines()}

		_, err = e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, machines, nil, nil)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.deletedMachines()).To(BeEmpty())

		resized := conditions.Get(e.tcp, string(controlplanev1.ResizedCondition))
		g.Expect(resized).NotTo(BeNil())
		g.Expect(resized.Reason).To(Equal(controlplanev1.ScalingDownReason))
		g.Expect(strings.ToLower(resized.Message)).To(ContainSubstring("remediation"))
	})
}

func TestMachineToBeRemediatedNeedsFailedHealthCheck(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	// OwnerRemediated=False alone, without a failed health check, doesn't make a Machine a remediation target.
	for _, status := range []metav1.ConditionStatus{metav1.ConditionUnknown, metav1.ConditionTrue} {
		m := newTestMachine("m1", now, withUnhealthy())
		conditions.Set(m, metav1.Condition{
			Type:   clusterv1.MachineHealthCheckSucceededCondition,
			Status: status,
			Reason: "Test",
		})

		g.Expect(machineToBeRemediated([]*clusterv1.Machine{m})).To(BeNil(), "HealthCheckSucceeded=%s", status)
	}

	m := newTestMachine("m1", now, withUnhealthy())
	conditions.Delete(m, clusterv1.MachineHealthCheckSucceededCondition)

	g.Expect(machineToBeRemediated([]*clusterv1.Machine{m})).To(BeNil(), "HealthCheckSucceeded unset")
}

func TestRemediationIgnoresUnhealthyProvisioningMachine(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	// m3 failed to provision and is unhealthy too: it doesn't defer the remediation of m1, which is remediated first
	// because of the remediate-machine annotation.
	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy(), withAnnotation(clusterv1.RemediateMachineAnnotation, "")),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour), withUnhealthy(), withoutNodeRef()),
	)
	e.etcd.members = etcdMembersFor("m1", "m2")

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeTrue())

	e.expectRemediated(g, "m1")
}

func TestRemediationSkipsTargetAlreadyDeleting(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")

	g.Expect(e.client.Delete(e.ctx, e.machine("m1"))).To(Succeed())

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeFalse())

	g.Expect(e.etcd.getCalls()).To(BeEmpty())
	g.Expect(e.tcp.Annotations).NotTo(HaveKey(remediationInProgressAnnotation))
	g.Expect(e.machine("m1").Annotations).NotTo(HaveKey(etcdLeavingAnnotation))
	g.Expect(conditions.Get(e.machine("m1"), clusterv1.MachineOwnerRemediatedCondition).Reason).To(Equal(clusterv1.MachineOwnerRemediatedWaitingForRemediationReason))
}

func TestRemediationDefersWhenEtcdCantBeInspected(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.listErr = errors.New("connection refused")

	remediated, err := e.remediate()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(remediated).To(BeFalse())

	e.expectNotRemediated(g, "m1", remediationDeferredReason, "Failed to check etcd cluster health")
	g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
}

func TestRemediationInProgressKeptWhileRemediatedMachineExists(t *testing.T) {
	g := NewWithT(t)
	now := time.Now().Truncate(time.Second)

	value, err := (&remediationData{Machine: "m0", Timestamp: metav1.NewTime(now.Add(-remediationInProgressTimeout - time.Minute))}).marshal()
	g.Expect(err).NotTo(HaveOccurred())

	tcp := newTestTCP(3)
	tcp.Annotations = map[string]string{remediationInProgressAnnotation: value}

	// m0 is still being deleted after the timeout, so no replacement was created yet.
	e := newRemediationEnv(t, tcp,
		newTestMachine("m0", now.Add(-4*time.Hour), withUnhealthy()),
		newTestMachine("m1", now.Add(-3*time.Hour)),
		newTestMachine("m2", now.Add(-2*time.Hour)),
	)
	g.Expect(e.client.Delete(e.ctx, e.machine("m0"))).To(Succeed())

	_, err = e.remediate()
	g.Expect(err).NotTo(HaveOccurred())

	// The remediation data must survive until it can be recorded on the replacement.
	g.Expect(e.tcp.Annotations).To(HaveKeyWithValue(remediationInProgressAnnotation, value))
}

func TestScaleDownKeepsMachineWhenEtcdMemberCantBeRemoved(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(2),
		newTestMachine("m1", now.Add(-3*time.Hour)),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.members = etcdMembersFor("m1", "m2", "m3")
	e.etcd.leaveErr = errors.New("connection refused")
	e.etcd.removeErr = errors.New("context deadline exceeded")

	machines := e.machines()
	ptrs := []*clusterv1.Machine{&machines[0], &machines[1], &machines[2]}

	err := e.r.removeFromEtcdAndDelete(e.ctx, e.tcp, ptrs[0], ptrs)
	g.Expect(err).To(HaveOccurred())

	g.Expect(e.deletedMachines()).To(BeEmpty())
	g.Expect(e.machine("m1").Annotations).NotTo(HaveKey(etcdLeavingAnnotation))

	resized := conditions.Get(e.tcp, string(controlplanev1.ResizedCondition))
	g.Expect(resized).NotTo(BeNil())
	g.Expect(resized.Reason).To(Equal(controlplanev1.ScalingDownReason))
	g.Expect(resized.Message).To(ContainSubstring("Failed to remove machine m1 from etcd"))
}

func TestEtcdLeavingMachines(t *testing.T) {
	now := time.Now()

	t.Run("a machine that left etcd is deleted after its deletion failed", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy()),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")
		e.etcd.unhealthy["m1"] = true

		// m1 leaves etcd, but deleting it fails.
		e.r.Client = interceptor.NewClient(e.client.(client.WithWatch), interceptor.Funcs{
			Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
				return errors.New("injected delete failure")
			},
		})

		remediated, err := e.remediate()
		g.Expect(err).To(HaveOccurred())
		g.Expect(remediated).To(BeTrue())
		g.Expect(e.deletedMachines()).To(BeEmpty())
		g.Expect(e.machine("m1").Annotations).To(HaveKeyWithValue(etcdLeavingAnnotation, "true"))

		// The remediation stays recorded, since m1 left etcd and is deleted later.
		data, err := remediationDataFromAnnotation(e.storedTCP().Annotations[remediationInProgressAnnotation])
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(data.Machine).To(Equal("m1"))

		e.r.Client = e.client

		// m1 recovers, so it is not a remediation target anymore, and m2 becomes unhealthy.
		m1 := e.machine("m1")
		conditions.Set(m1, metav1.Condition{
			Type:   clusterv1.MachineHealthCheckSucceededCondition,
			Status: metav1.ConditionTrue,
			Reason: clusterv1.MachineHealthCheckSucceededReason,
		})
		g.Expect(e.client.Status().Update(e.ctx, m1)).To(Succeed())

		m2 := e.machine("m2")
		withUnhealthy()(m2)
		g.Expect(e.client.Status().Update(e.ctx, m2)).To(Succeed())

		e.etcd.members = etcdMembersFor("m2", "m3")
		e.etcd.unhealthy = map[string]bool{}

		_, err = e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, nil)
		g.Expect(err).NotTo(HaveOccurred())

		// m1 is deleted first; m2 is only remediated once m1 is gone.
		g.Expect(e.deletedMachines()).To(ConsistOf("m1"))
		g.Expect(e.etcd.getCalls()).To(ContainElement(HavePrefix("list:")))
	})

	t.Run("the mark is removed from a machine that is still an etcd member", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-3*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.members = etcdMembersFor("m1", "m2", "m3")

		_, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, nil)
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(e.deletedMachines()).To(BeEmpty())
		g.Expect(e.machine("m1").Annotations).NotTo(HaveKey(etcdLeavingAnnotation))
	})

	t.Run("the machine is kept while etcd can't be inspected", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(3),
			newTestMachine("m1", now.Add(-3*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.listErr = errors.New("connection refused")

		_, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, nil)
		g.Expect(err).To(HaveOccurred())

		g.Expect(e.deletedMachines()).To(BeEmpty())
		g.Expect(e.machine("m1").Annotations).To(HaveKeyWithValue(etcdLeavingAnnotation, "true"))
	})

	t.Run("the control plane is still scaled up while etcd can't be inspected", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(4),
			newTestMachine("m1", now.Add(-3*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.listErr = errors.New("connection refused")

		_, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, nil)
		g.Expect(err).To(MatchError(ContainSubstring("failed to check whether machine m1 left etcd")))

		// The scale up ran; creating the Machine fails here because there is no infrastructure template.
		resized := conditions.Get(e.tcp, string(controlplanev1.ResizedCondition))
		g.Expect(resized).NotTo(BeNil())
		g.Expect(resized.Reason).To(Equal(controlplanev1.ScalingUpReason))

		g.Expect(e.deletedMachines()).To(BeEmpty())
		g.Expect(e.machine("m1").Annotations).To(HaveKeyWithValue(etcdLeavingAnnotation, "true"))
	})

	t.Run("the control plane is not scaled down while a machine is leaving etcd", func(t *testing.T) {
		g := NewWithT(t)

		e := newRemediationEnv(t, newTestTCP(2),
			newTestMachine("m1", now.Add(-3*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
			newTestMachine("m2", now.Add(-2*time.Hour)),
			newTestMachine("m3", now.Add(-time.Hour)),
		)
		e.etcd.listErr = errors.New("connection refused")

		result, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, nil)
		g.Expect(err).To(MatchError(ContainSubstring("failed to check whether machine m1 left etcd")))
		g.Expect(result.RequeueAfter).NotTo(BeZero())

		resized := conditions.Get(e.tcp, string(controlplanev1.ResizedCondition))
		g.Expect(resized).NotTo(BeNil())
		g.Expect(resized.Reason).To(Equal(controlplanev1.ScalingDownReason))
		g.Expect(resized.Message).To(ContainSubstring("Waiting for machine m1 to leave etcd"))

		g.Expect(e.deletedMachines()).To(BeEmpty())
	})
}

func TestRemediationWaitsWhileTargetIsLeavingEtcd(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	// m1 left etcd, but deleting it failed; it is also unhealthy. Checking whether it left etcd fails, while the etcd
	// health check of this reconcile could still inspect m2 and m3.
	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withUnhealthy(), withAnnotation(etcdLeavingAnnotation, "true")),
		newTestMachine("m2", now.Add(-2*time.Hour)),
		newTestMachine("m3", now.Add(-time.Hour)),
	)
	e.etcd.listErr = errors.New("connection refused")
	e.etcd.leaveErr = errors.New("etcd is not running")

	observed := map[string]etcdMachineStatus{
		"m2": {healthy: true, members: etcdMembersFor("m2", "m3")},
		"m3": {healthy: true, members: etcdMembersFor("m2", "m3")},
	}

	_, err := e.r.reconcileRemediationAndMachines(e.ctx, e.cluster, e.tcp, &clusterv1.MachineList{Items: e.machines()}, nil, observed)
	g.Expect(err).To(MatchError(ContainSubstring("failed to check whether machine m1 left etcd")))

	e.expectNotRemediated(g, "m1", remediationDeferredReason, "removal of this machine from etcd")
	g.Expect(e.machine("m1").Annotations).To(HaveKeyWithValue(etcdLeavingAnnotation, "true"))
	g.Expect(e.etcd.getCalls()).NotTo(ContainElement(HavePrefix("leave:")))
}

func TestRemoveMachineFromEtcdKeepsAnEarlierLeavingMark(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	e := newRemediationEnv(t, newTestTCP(3),
		newTestMachine("m1", now.Add(-3*time.Hour), withAnnotation(etcdLeavingAnnotation, "true")),
		newTestMachine("m2", now.Add(-2*time.Hour)),
	)
	e.etcd.leaveErr = errors.New("etcd is not running")
	e.etcd.listErr = errors.New("connection refused")

	machines := e.machines()

	err := e.r.removeMachineFromEtcd(e.ctx, e.tcp, &machines[0], []*clusterv1.Machine{&machines[0], &machines[1]})
	g.Expect(err).To(HaveOccurred())

	// The member may have left etcd when the mark was set: only reconcileEtcdLeavingMachines may remove it.
	g.Expect(e.machine("m1").Annotations).To(HaveKeyWithValue(etcdLeavingAnnotation, "true"))
}

func TestOtherEtcdMachines(t *testing.T) {
	g := NewWithT(t)
	now := time.Now()

	deleting := newTestMachine("deleting", now)
	deleting.DeletionTimestamp = ptr.To(metav1.Now())

	self := newTestMachine("self", now)
	machines := []*clusterv1.Machine{
		newTestMachine("a", now),
		self,
		deleting,
		newTestMachine("leaving", now, withAnnotation(etcdLeavingAnnotation, "true")),
		newTestMachine("provisioning", now, withoutNodeRef()),
		newTestMachine("b", now),
	}

	names := func(machines []*clusterv1.Machine) []string {
		var names []string

		for _, m := range machines {
			names = append(names, m.Name)
		}

		return names
	}

	g.Expect(names(slices.Collect(otherEtcdMachines(self, machines)))).To(Equal([]string{"a", "b"}))

	// Iterating stops when asked to.
	for m := range otherEtcdMachines(self, machines) {
		g.Expect(m.Name).To(Equal("a"))

		break
	}
}
