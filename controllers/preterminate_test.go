// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// fakeEtcdCalls stands in for a Talos client pointed at a single machine.
type fakeEtcdCalls struct {
	name string

	members        []*machineapi.EtcdMember
	memberListErr  error
	services       []talosclient.ServiceInfo
	serviceInfoErr error
	forfeitErr     error
	leaveErr       error
	removeErr      error

	serviceInfoCalls int
	memberListCalls  int
	forfeitCalls     int
	leaveCalls       int
	removedIDs       []uint64
	closed           bool
}

func (f *fakeEtcdCalls) ServiceInfo(_ context.Context, _ string, _ ...grpc.CallOption) ([]talosclient.ServiceInfo, error) {
	f.serviceInfoCalls++

	if f.serviceInfoErr != nil {
		return nil, f.serviceInfoErr
	}

	return f.services, nil
}

func (f *fakeEtcdCalls) EtcdMemberList(_ context.Context, _ *machineapi.EtcdMemberListRequest, _ ...grpc.CallOption) (*machineapi.EtcdMemberListResponse, error) {
	f.memberListCalls++

	if f.memberListErr != nil {
		return nil, f.memberListErr
	}

	return &machineapi.EtcdMemberListResponse{
		Messages: []*machineapi.EtcdMembers{
			{Members: f.members},
		},
	}, nil
}

func (f *fakeEtcdCalls) EtcdForfeitLeadership(_ context.Context, _ *machineapi.EtcdForfeitLeadershipRequest, _ ...grpc.CallOption) (*machineapi.EtcdForfeitLeadershipResponse, error) {
	f.forfeitCalls++

	if f.forfeitErr != nil {
		return nil, f.forfeitErr
	}

	return &machineapi.EtcdForfeitLeadershipResponse{}, nil
}

func (f *fakeEtcdCalls) EtcdLeaveCluster(_ context.Context, _ *machineapi.EtcdLeaveClusterRequest, _ ...grpc.CallOption) error {
	f.leaveCalls++

	return f.leaveErr
}

func (f *fakeEtcdCalls) EtcdRemoveMemberByID(_ context.Context, req *machineapi.EtcdRemoveMemberByIDRequest, _ ...grpc.CallOption) error {
	if f.removeErr != nil {
		return f.removeErr
	}

	f.removedIDs = append(f.removedIDs, req.MemberId)

	return nil
}

func (f *fakeEtcdCalls) Close() error {
	f.closed = true

	return nil
}

// fakeEtcdDialer hands out a fakeEtcdCalls per machine name, or an error for machines that
// stand in for an unreachable node.
type fakeEtcdDialer struct {
	clients  map[string]*fakeEtcdCalls
	dialErrs map[string]error

	dialed []string
}

func newFakeEtcdDialer() *fakeEtcdDialer {
	return &fakeEtcdDialer{
		clients:  map[string]*fakeEtcdCalls{},
		dialErrs: map[string]error{},
	}
}

func (d *fakeEtcdDialer) dial(_ context.Context, _ *controlplanev1.TalosControlPlane, machines ...clusterv1.Machine) (etcdCalls, error) {
	if len(machines) != 1 {
		return nil, fmt.Errorf("expected exactly one machine, got %d", len(machines))
	}

	name := machines[0].Name

	d.dialed = append(d.dialed, name)

	if err := d.dialErrs[name]; err != nil {
		return nil, err
	}

	c, ok := d.clients[name]
	if !ok {
		return nil, fmt.Errorf("no fake etcd client configured for %q", name)
	}

	return c, nil
}

func (d *fakeEtcdDialer) totalEtcdCalls() int {
	total := 0

	for _, c := range d.clients {
		total += c.serviceInfoCalls + c.memberListCalls + c.forfeitCalls + c.leaveCalls + len(c.removedIDs)
	}

	return total
}

// --- fixture ---------------------------------------------------------------

type preTerminateFixture struct {
	r        *TalosControlPlaneReconciler
	cluster  *clusterv1.Cluster
	tcp      *controlplanev1.TalosControlPlane
	machines *clusterv1.MachineList
	dialer   *fakeEtcdDialer
	recorder *record.FakeRecorder
}

func preTerminateScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, clusterv1.AddToScheme(scheme))
	require.NoError(t, cabptv1.AddToScheme(scheme))
	require.NoError(t, controlplanev1.AddToScheme(scheme))

	return scheme
}

func newPreTerminateTCP() *controlplanev1.TalosControlPlane {
	return &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp",
			Namespace: "default",
			UID:       "tcp-uid",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "test"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       "test",
			}},
			Finalizers: []string{controlplanev1.TalosControlPlaneFinalizer},
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.34.0",
			ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
				ControlPlaneConfig: cabptv1.TalosConfigSpec{GenerateType: "controlplane"},
			},
		},
	}
}

func newPTMachine(tcp *controlplanev1.TalosControlPlane, name string, mutators ...func(*clusterv1.Machine)) *clusterv1.Machine {
	m := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         "test",
				clusterv1.MachineControlPlaneLabel: "",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{ClusterName: "test"},
		Status: clusterv1.MachineStatus{
			NodeRef:   clusterv1.MachineNodeReference{Name: name},
			Addresses: clusterv1.MachineAddresses{{Type: clusterv1.MachineInternalIP, Address: "10.0.0.1"}},
		},
	}

	for _, mutate := range mutators {
		mutate(m)
	}

	return m
}

func ptHooked(m *clusterv1.Machine) {
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}

	m.Annotations[PreTerminateHookCleanupAnnotation] = ""
}

// ptDeleting marks the machine as deleting with the given Deleting condition reason. The
// finalizer is required: the fake client refuses objects that carry a deletionTimestamp
// without one.
func ptDeleting(at time.Time, reason string) func(*clusterv1.Machine) {
	return func(m *clusterv1.Machine) {
		ts := metav1.NewTime(at)
		m.DeletionTimestamp = &ts
		m.Finalizers = append(m.Finalizers, clusterv1.MachineFinalizer)

		if reason != "" {
			m.Status.Conditions = append(m.Status.Conditions, metav1.Condition{
				Type:               clusterv1.MachineDeletingCondition,
				Status:             metav1.ConditionTrue,
				Reason:             reason,
				LastTransitionTime: metav1.NewTime(at),
			})
		}
	}
}

func ptObservedAt(at time.Time) func(*clusterv1.Machine) {
	return func(m *clusterv1.Machine) {
		if m.Annotations == nil {
			m.Annotations = map[string]string{}
		}

		m.Annotations[etcdCleanupObservedAtAnnotation] = at.Format(time.RFC3339)
	}
}

func newPreTerminateFixture(t *testing.T, machines ...*clusterv1.Machine) *preTerminateFixture {
	t.Helper()

	return newPreTerminateFixtureWithTCP(t, newPreTerminateTCP(), machines...)
}

func newPreTerminateFixtureWithTCP(t *testing.T, tcp *controlplanev1.TalosControlPlane, machines ...*clusterv1.Machine) *preTerminateFixture {
	t.Helper()

	scheme := preTerminateScheme(t)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	objs := []client.Object{tcp, cluster}
	list := &clusterv1.MachineList{}

	for _, m := range machines {
		objs = append(objs, m)
		list.Items = append(list.Items, *m)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&clusterv1.Machine{}, &clusterv1.Cluster{}, &controlplanev1.TalosControlPlane{}).
		Build()

	dialer := newFakeEtcdDialer()
	recorder := record.NewFakeRecorder(64)

	return &preTerminateFixture{
		r: &TalosControlPlaneReconciler{
			Client:                        cl,
			APIReader:                     cl,
			Log:                           ctrl.Log.WithName("test"),
			Scheme:                        scheme,
			Recorder:                      recorder,
			EnableMachinePreTerminateHook: true,
			EtcdCleanupTimeout:            2 * time.Minute,
			etcdDialer:                    dialer.dial,
		},
		cluster:  cluster,
		tcp:      tcp,
		machines: list,
		dialer:   dialer,
		recorder: recorder,
	}
}

func (f *preTerminateFixture) run(ctx context.Context) (ctrl.Result, error) {
	return f.r.reconcileMachinePreTerminateHooks(ctx, f.cluster, f.tcp, f.machines)
}

func (f *preTerminateFixture) get(t *testing.T, name string) *clusterv1.Machine {
	t.Helper()

	var m clusterv1.Machine

	require.NoError(t, f.r.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, &m))

	return &m
}

func (f *preTerminateFixture) hasHook(t *testing.T, name string) bool {
	t.Helper()

	_, ok := f.get(t, name).Annotations[PreTerminateHookCleanupAnnotation]

	return ok
}

func (f *preTerminateFixture) events() []string {
	var out []string

	for {
		select {
		case e := <-f.recorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func runningEtcd() []talosclient.ServiceInfo {
	return []talosclient.ServiceInfo{
		{Service: &machineapi.ServiceInfo{Id: "etcd", State: "Running"}},
	}
}

// --- 1H.1: stamping --------------------------------------------------------

func TestDesiredMachineAnnotations_StampsHookWhenEnabled(t *testing.T) {
	f := newPreTerminateFixture(t)

	annotations := f.r.desiredMachineAnnotations(f.tcp)

	assert.Contains(t, annotations, PreTerminateHookCleanupAnnotation)
	assert.Equal(t, "", annotations[PreTerminateHookCleanupAnnotation])
}

func TestDesiredMachineAnnotations_NoStampWhenDisabled(t *testing.T) {
	f := newPreTerminateFixture(t)
	f.r.EnableMachinePreTerminateHook = false

	assert.NotContains(t, f.r.desiredMachineAnnotations(f.tcp), PreTerminateHookCleanupAnnotation)
}

func TestDesiredMachineAnnotations_KeepsTemplateAnnotations(t *testing.T) {
	f := newPreTerminateFixture(t)
	f.tcp.Spec.MachineTemplate.ObjectMeta.Annotations = map[string]string{"team": "sidero"}

	annotations := f.r.desiredMachineAnnotations(f.tcp)

	assert.Equal(t, "sidero", annotations["team"])
	assert.Contains(t, annotations, PreTerminateHookCleanupAnnotation)
	assert.NotContains(t, f.tcp.Spec.MachineTemplate.ObjectMeta.Annotations, PreTerminateHookCleanupAnnotation,
		"the template annotations must not be mutated")
}

func TestPreTerminateHook_StampsAdoptedMachines(t *testing.T) {
	f := newPreTerminateFixture(t, newPTMachine(newPreTerminateTCP(), "adopted"))

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.True(t, f.hasHook(t, "adopted"))
}

func TestPreTerminateHook_NoStampWhenFlagOff(t *testing.T) {
	f := newPreTerminateFixture(t, newPTMachine(newPreTerminateTCP(), "cp-1"))
	f.r.EnableMachinePreTerminateHook = false

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"))
}

func TestPreTerminateHook_NoStampOnDeletingMachine(t *testing.T) {
	tcp := newPreTerminateTCP()
	deleting := newPTMachine(tcp, "cp-1", ptDeleting(time.Now(), clusterv1.MachineDeletingDrainingNodeReason))

	f := newPreTerminateFixture(t, deleting)

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"),
		"a machine already in the deletion pipeline must not be stamped: it would race the phase gate")
}

// --- 1H.11: only machines owned by this control plane ----------------------

func TestPreTerminateHook_IgnoresMachinesNotOwnedByTCP(t *testing.T) {
	tcp := newPreTerminateTCP()

	foreign := newPTMachine(tcp, "worker-owned-cp")
	foreign.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "MachineSet",
		Name:       "other",
		UID:        "other-uid",
		Controller: ptr.To(true),
	}}

	f := newPreTerminateFixture(t, foreign)

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "worker-owned-cp"))
}

// --- 1H.2: phase gate ------------------------------------------------------

func TestPreTerminateHook_WaitsForPreTerminatePhase(t *testing.T) {
	for _, reason := range []string{
		clusterv1.MachineDeletingWaitingForPreDrainHookReason,
		clusterv1.MachineDeletingDrainingNodeReason,
		clusterv1.MachineDeletingWaitingForVolumeDetachReason,
	} {
		t.Run(reason, func(t *testing.T) {
			tcp := newPreTerminateTCP()
			victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), reason))
			peer := newPTMachine(tcp, "cp-2", ptHooked)

			f := newPreTerminateFixture(t, victim, peer)

			res, err := f.run(context.Background())
			require.NoError(t, err)

			assert.Positive(t, res.RequeueAfter, "must requeue until CAPI reaches the pre-terminate phase")
			assert.True(t, f.hasHook(t, "cp-1"))
			assert.Zero(t, f.dialer.totalEtcdCalls(), "no etcd traffic before drain completes")
		})
	}
}

// --- 1H.3: victim is not an etcd member ------------------------------------

func TestPreTerminateHook_ReleasesWhenNotAMember(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:    "cp-2",
		members: []*machineapi.EtcdMember{{Id: 2, Hostname: "cp-2"}},
	}

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"))
	assert.NotContains(t, f.dialer.dialed, "cp-1", "the victim is never dialed when it is not a member")
	assert.Empty(t, f.dialer.clients["cp-2"].removedIDs)
	assert.Contains(t, strings.Join(f.events(), "\n"), "EtcdCleanupSkipped")
}

// --- 1H.4: graceful leave --------------------------------------------------

func TestPreTerminateHook_GracefulLeaveFromVictim(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name: "cp-2",
		members: []*machineapi.EtcdMember{
			{Id: 1, Hostname: "cp-1"},
			{Id: 2, Hostname: "cp-2"},
		},
	}
	f.dialer.clients["cp-1"] = &fakeEtcdCalls{name: "cp-1", services: runningEtcd()}

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, f.dialer.clients["cp-1"].forfeitCalls)
	assert.Equal(t, 1, f.dialer.clients["cp-1"].leaveCalls)
	assert.Empty(t, f.dialer.clients["cp-2"].removedIDs, "the peer must not be asked to remove a member that left")
	assert.False(t, f.hasHook(t, "cp-1"))
	assert.Contains(t, strings.Join(f.events(), "\n"), "EtcdMemberLeft")
	assert.True(t, f.dialer.clients["cp-1"].closed, "the per-call client is closed")
}

// --- 1H.5: victim unreachable, remove via peer -----------------------------

func TestPreTerminateHook_RemovesViaPeerWhenVictimUnreachable(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name: "cp-2",
		members: []*machineapi.EtcdMember{
			{Id: 1, Hostname: "cp-1"},
			{Id: 2, Hostname: "cp-2"},
		},
	}
	f.dialer.dialErrs["cp-1"] = fmt.Errorf("connection refused")

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []uint64{1}, f.dialer.clients["cp-2"].removedIDs)
	assert.False(t, f.hasHook(t, "cp-1"))
	assert.Contains(t, strings.Join(f.events(), "\n"), "EtcdMemberRemovedViaPeer")
}

func TestPreTerminateHook_RemovesViaPeerWhenLeaveFails(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:    "cp-2",
		members: []*machineapi.EtcdMember{{Id: 1, Hostname: "cp-1"}, {Id: 2, Hostname: "cp-2"}},
	}
	f.dialer.clients["cp-1"] = &fakeEtcdCalls{
		name:     "cp-1",
		services: runningEtcd(),
		leaveErr: fmt.Errorf("etcd is shutting down"),
	}

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []uint64{1}, f.dialer.clients["cp-2"].removedIDs)
	assert.False(t, f.hasHook(t, "cp-1"))
}

// --- 1H.6: fail-open deadline ----------------------------------------------

func TestPreTerminateHook_RetainsHookBeforeDeadline(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:      "cp-2",
		members:   []*machineapi.EtcdMember{{Id: 1, Hostname: "cp-1"}, {Id: 2, Hostname: "cp-2"}},
		removeErr: fmt.Errorf("etcd unavailable"),
	}
	f.dialer.dialErrs["cp-1"] = fmt.Errorf("connection refused")

	res, err := f.run(context.Background())
	require.Error(t, err)
	assert.Positive(t, res.RequeueAfter)
	assert.True(t, f.hasHook(t, "cp-1"), "the hook is held while there is still time to clean up")

	observed := f.get(t, "cp-1").Annotations[etcdCleanupObservedAtAnnotation]
	assert.NotEmpty(t, observed, "the deadline anchor is stamped on the first qualifying visit")
}

func TestPreTerminateHook_FailsOpenPastDeadline(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1",
		ptHooked,
		ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason),
		ptObservedAt(time.Now().Add(-10*time.Minute)),
	)
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:      "cp-2",
		members:   []*machineapi.EtcdMember{{Id: 1, Hostname: "cp-1"}, {Id: 2, Hostname: "cp-2"}},
		removeErr: fmt.Errorf("etcd unavailable"),
	}
	f.dialer.dialErrs["cp-1"] = fmt.Errorf("connection refused")

	_, err := f.run(context.Background())
	require.NoError(t, err, "past the deadline the deletion is never parked")

	assert.False(t, f.hasHook(t, "cp-1"))

	events := strings.Join(f.events(), "\n")
	assert.Contains(t, events, "EtcdCleanupOrphaned")
	assert.Contains(t, events, "Warning")
}

// --- 1H.7: serialization ---------------------------------------------------

func TestPreTerminateHook_ProcessesOnlyTheOldestDeletingMachine(t *testing.T) {
	tcp := newPreTerminateTCP()
	now := time.Now()

	older := newPTMachine(tcp, "cp-older", ptHooked, ptDeleting(now.Add(-time.Minute), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	newer := newPTMachine(tcp, "cp-newer", ptHooked, ptDeleting(now, clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-peer", ptHooked)

	f := newPreTerminateFixture(t, newer, older, peer)
	f.dialer.clients["cp-peer"] = &fakeEtcdCalls{
		name: "cp-peer",
		members: []*machineapi.EtcdMember{
			{Id: 1, Hostname: "cp-older"},
			{Id: 2, Hostname: "cp-newer"},
			{Id: 3, Hostname: "cp-peer"},
		},
	}
	f.dialer.clients["cp-older"] = &fakeEtcdCalls{name: "cp-older", services: runningEtcd()}
	f.dialer.clients["cp-newer"] = &fakeEtcdCalls{name: "cp-newer", services: runningEtcd()}

	res, err := f.run(context.Background())
	require.NoError(t, err)

	assert.Positive(t, res.RequeueAfter, "the second machine is picked up on a later pass")
	assert.False(t, f.hasHook(t, "cp-older"))
	assert.True(t, f.hasHook(t, "cp-newer"), "one etcd membership change at a time")
	assert.Equal(t, 1, f.dialer.clients["cp-older"].leaveCalls)
	assert.Zero(t, f.dialer.clients["cp-newer"].leaveCalls)
}

// --- 1H.8: cluster / control plane teardown --------------------------------

func TestPreTerminateHook_ReleasesWithoutEtcdOpsWhenClusterIsDeleting(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)

	ts := metav1.Now()
	f.cluster.DeletionTimestamp = &ts

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"))
	assert.Empty(t, f.dialer.dialed, "final-quorum teardown never touches etcd membership")
}

func TestReconcileDelete_ReleasesHooksAndConverges(t *testing.T) {
	tcp := newPreTerminateTCP()
	ts := metav1.Now()
	tcp.DeletionTimestamp = &ts

	cp1 := newPTMachine(tcp, "cp-1", ptHooked)
	cp2 := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixtureWithTCP(t, tcp, cp1, cp2)

	res, err := f.r.reconcileDelete(context.Background(), f.cluster, f.tcp)
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)
	assert.Empty(t, f.dialer.dialed, "control plane teardown never touches etcd membership")

	var remaining clusterv1.MachineList
	require.NoError(t, f.r.Client.List(context.Background(), &remaining))
	assert.Empty(t, remaining.Items, "machines without a finalizer are gone once the hook is released")

	// Second pass: no machines left, the control plane finalizer is dropped.
	_, err = f.r.reconcileDelete(context.Background(), f.cluster, f.tcp)
	require.NoError(t, err)
	assert.NotContains(t, f.tcp.Finalizers, controlplanev1.TalosControlPlaneFinalizer)
}

// A deleting machine without our hook has unknown etcd state, so nothing else may have its
// member removed until that machine is gone: doing both at once is how a three-member cluster
// ends up one-live-of-two and loses quorum.
func TestPreTerminateHook_HoldsWhileAnUnhookedMachineIsDeleting(t *testing.T) {
	tcp := newPreTerminateTCP()
	now := time.Now()

	hooked := newPTMachine(tcp, "cp-hooked", ptHooked, ptDeleting(now.Add(-time.Minute), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	unhooked := newPTMachine(tcp, "cp-unhooked", ptDeleting(now, clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-peer", ptHooked)

	f := newPreTerminateFixture(t, hooked, unhooked, peer)
	f.dialer.clients["cp-peer"] = &fakeEtcdCalls{
		name:    "cp-peer",
		members: []*machineapi.EtcdMember{{Id: 1, Hostname: "cp-hooked"}, {Id: 2, Hostname: "cp-unhooked"}, {Id: 3, Hostname: "cp-peer"}},
	}
	f.dialer.clients["cp-hooked"] = &fakeEtcdCalls{name: "cp-hooked", services: runningEtcd()}

	res, err := f.run(context.Background())
	require.NoError(t, err)

	assert.Positive(t, res.RequeueAfter)
	assert.True(t, f.hasHook(t, "cp-hooked"), "the hooked machine waits its turn")
	assert.Zero(t, f.dialer.totalEtcdCalls(), "no membership change while another member's fate is unknown")
}

// A stamp patch that keeps failing must not park a deletion that is already in flight.
func TestPreTerminateHook_StampFailureDoesNotBlockServicing(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:    "cp-2",
		members: []*machineapi.EtcdMember{{Id: 2, Hostname: "cp-2"}},
	}

	// A machine the reconcile sees but the API server does not: stamping it patches an object
	// that is not there, which fails the way a persistently rejected patch would.
	phantom := newPTMachine(tcp, "cp-3")
	f.machines.Items = append(f.machines.Items, *phantom)

	_, err := f.run(context.Background())
	require.Error(t, err, "the stamping failure is still reported")

	assert.False(t, f.hasHook(t, "cp-1"), "the in-flight deletion is serviced regardless")
}

// --- 1H.9: last member -----------------------------------------------------

func TestPreTerminateHook_SkipsLeaveForLastMember(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))

	f := newPreTerminateFixture(t, victim)

	_, err := f.run(context.Background())
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"))
	assert.Empty(t, f.dialer.dialed, "there is nobody left to remove the member from")
	assert.Contains(t, strings.Join(f.events(), "\n"), "EtcdCleanupSkipped")
}

// --- 1H.10: scale down -----------------------------------------------------

func TestDeleteControlPlaneMachine_HookedVictimSkipsInlineLeave(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked)

	f := newPreTerminateFixture(t, victim)

	workload := fake.NewClientBuilder().WithScheme(preTerminateScheme(t)).Build()

	_, err := f.r.deleteControlPlaneMachine(context.Background(), workload, f.tcp, victim)
	require.NoError(t, err)

	assert.Empty(t, f.dialer.dialed, "the deletion pipeline owns etcd cleanup for hooked machines")

	var remaining clusterv1.MachineList
	require.NoError(t, f.r.Client.List(context.Background(), &remaining))
	assert.Empty(t, remaining.Items)
}

func TestDeleteControlPlaneMachine_UnhookedVictimLeavesInline(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1")

	f := newPreTerminateFixture(t, victim)
	f.dialer.clients["cp-1"] = &fakeEtcdCalls{name: "cp-1", services: runningEtcd()}

	workload := fake.NewClientBuilder().WithScheme(preTerminateScheme(t)).Build()

	_, err := f.r.deleteControlPlaneMachine(context.Background(), workload, f.tcp, victim)
	require.NoError(t, err)

	assert.Equal(t, []string{"cp-1"}, f.dialer.dialed)
	assert.Equal(t, 1, f.dialer.clients["cp-1"].leaveCalls)
}

func TestDeleteControlPlaneMachine_UnhookedVictimPropagatesLeaveFailure(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1")

	f := newPreTerminateFixture(t, victim)
	f.dialer.clients["cp-1"] = &fakeEtcdCalls{
		name:     "cp-1",
		services: runningEtcd(),
		leaveErr: fmt.Errorf("etcd refused the leave"),
	}

	workload := fake.NewClientBuilder().WithScheme(preTerminateScheme(t)).Build()

	_, err := f.r.deleteControlPlaneMachine(context.Background(), workload, f.tcp, victim)
	require.Error(t, err, "a failed inline leave must not be swallowed")
	assert.Contains(t, err.Error(), "etcd refused the leave")

	// The machine is still deleted: the delete request precedes the error return today and
	// that behaviour is unchanged.
	var remaining clusterv1.MachineList
	require.NoError(t, f.r.Client.List(context.Background(), &remaining))
	assert.Empty(t, remaining.Items)
}

// Losing the Talos connection must not delete the machine out from under a member that is still
// in the etcd cluster: the legacy path aborts before the delete request, as it always has.
func TestDeleteControlPlaneMachine_UnhookedVictimAbortsWhenTalosIsUnreachable(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1")

	f := newPreTerminateFixture(t, victim)
	f.dialer.dialErrs["cp-1"] = fmt.Errorf("talosconfig secret is missing")

	workload := fake.NewClientBuilder().WithScheme(preTerminateScheme(t)).Build()

	res, err := f.r.deleteControlPlaneMachine(context.Background(), workload, f.tcp, victim)
	require.Error(t, err)
	assert.Positive(t, res.RequeueAfter)

	var remaining clusterv1.MachineList
	require.NoError(t, f.r.Client.List(context.Background(), &remaining))
	assert.Len(t, remaining.Items, 1)
}

// The handler is wired ahead of every gate that can park the reconcile: a cluster that has not
// published a control plane endpoint yet must still be able to finish a machine deletion.
func TestReconcile_ServicesHooksBeforeTheControlPlaneEndpointGate(t *testing.T) {
	tcp := newPreTerminateTCP()
	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixture(t, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:    "cp-2",
		members: []*machineapi.EtcdMember{{Id: 2, Hostname: "cp-2"}},
	}

	require.False(t, f.cluster.Spec.ControlPlaneEndpoint.IsValid())

	_, err := f.r.reconcile(context.Background(), f.cluster, f.tcp)
	require.NoError(t, err)

	assert.False(t, f.hasHook(t, "cp-1"))
}

// Same, for the infrastructure template: a control plane whose template has been deleted still
// has to be able to finish a machine deletion. Before the handler ran first, this parked the
// deletion forever with the fail-open clock never started.
func TestReconcile_ServicesHooksWhenTheInfraTemplateIsMissing(t *testing.T) {
	tcp := newPreTerminateTCP()
	tcp.Spec.MachineTemplate.Spec.InfrastructureRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: "infrastructure.cluster.x-k8s.io",
		Kind:     "GenericInfrastructureMachineTemplate",
		Name:     "deleted-template",
	}

	victim := newPTMachine(tcp, "cp-1", ptHooked, ptDeleting(time.Now(), clusterv1.MachineDeletingWaitingForPreTerminateHookReason))
	peer := newPTMachine(tcp, "cp-2", ptHooked)

	f := newPreTerminateFixtureWithTCP(t, tcp, victim, peer)
	f.dialer.clients["cp-2"] = &fakeEtcdCalls{
		name:    "cp-2",
		members: []*machineapi.EtcdMember{{Id: 2, Hostname: "cp-2"}},
	}

	_, err := f.r.reconcile(context.Background(), f.cluster, f.tcp)
	require.Error(t, err, "the unresolvable template is still reported")

	assert.False(t, f.hasHook(t, "cp-1"), "the deletion is serviced before the template gate")
}
