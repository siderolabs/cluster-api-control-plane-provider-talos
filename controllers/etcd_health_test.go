// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"net"
	"time"

	. "github.com/onsi/gomega"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

// startBlackholeServer returns the address of a listener that accepts connections but never answers,
// like a machine that is powered off behind a load balancer or whose packets are dropped.
func (suite *ControllersSuite) startBlackholeServer() string {
	listener, err := net.Listen("tcp", "localhost:0")
	suite.Require().NoError(err)

	go func() {
		<-suite.ctx.Done()
		listener.Close() //nolint:errcheck
	}()

	go func() {
		var conns []net.Conn

		defer func() {
			for _, conn := range conns {
				conn.Close() //nolint:errcheck
			}
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			conns = append(conns, conn)
		}
	}()

	return listener.Addr().String()
}

// TestEtcdHealthcheck checks how the etcd health check tells a degraded etcd cluster from one that lost quorum.
func (suite *ControllersSuite) TestEtcdHealthcheck() {
	etcdMembers := func(hostname string, members ...*machine.EtcdMember) *machine.EtcdMemberListResponse {
		return &machine.EtcdMemberListResponse{
			Messages: []*machine.EtcdMembers{
				{
					Metadata: &common.Metadata{Hostname: hostname},
					Members:  members,
				},
			},
		}
	}

	suite.Run("an unreachable machine doesn't hide the health of the others", func() {
		g := NewWithT(suite.T())

		fakeClient := newFakeClient()
		cluster, tcp, _ := suite.setupCluster(fakeClient, "test-etcd-health-unreachable", pointer.Int32(3))

		// foo-b doesn't answer: checking it must not use up the time to check foo-c.
		msA, addrA := suite.startMachineServer()
		addrB := suite.startBlackholeServer()
		msC, addrC := suite.startMachineServer()

		g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

		members := []*machine.EtcdMember{
			{Id: 1, Hostname: "foo-a"},
			{Id: 2, Hostname: "foo-b"},
			{Id: 3, Hostname: "foo-c"},
		}

		var machines []clusterv1.Machine

		for _, m := range []struct {
			name    string
			address string
			ms      *machineService
		}{
			{"foo-a", addrA, msA},
			{"foo-b", addrB, nil},
			{"foo-c", addrC, msC},
		} {
			cpMachine, _ := createMachineNodePair(m.name, cluster, tcp, true, m.address)
			machines = append(machines, *cpMachine)

			if m.ms != nil {
				suite.setEtcdRunning(m.address)
				m.ms.setEtcdMembersResponse(etcdMembers(m.name, members...))
			}
		}

		r := newReconciler(fakeClient)

		start := time.Now()

		reason, err := r.EtcdHealthcheckReason(suite.ctx, tcp, machines)
		g.Expect(err).To(MatchError(ContainSubstring("foo-b")))
		g.Expect(reason).To(Equal(controlplanev1.EtcdClusterUnhealthyReason))
		g.Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
	})

	suite.Run("unreachable machines don't add up their timeouts", func() {
		g := NewWithT(suite.T())

		fakeClient := newFakeClient()
		cluster, tcp, _ := suite.setupCluster(fakeClient, "test-etcd-health-concurrent", pointer.Int32(3))

		msA, addrA := suite.startMachineServer()
		addrB := suite.startBlackholeServer()
		addrC := suite.startBlackholeServer()

		g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

		members := []*machine.EtcdMember{
			{Id: 1, Hostname: "foo-a"},
			{Id: 2, Hostname: "foo-b"},
			{Id: 3, Hostname: "foo-c"},
		}

		suite.setEtcdRunning(addrA)
		msA.setEtcdMembersResponse(etcdMembers("foo-a", members...))

		var machines []clusterv1.Machine

		for _, m := range []struct{ name, address string }{{"foo-a", addrA}, {"foo-b", addrB}, {"foo-c", addrC}} {
			cpMachine, _ := createMachineNodePair(m.name, cluster, tcp, true, m.address)
			machines = append(machines, *cpMachine)
		}

		r := newReconciler(fakeClient)

		start := time.Now()

		healthy, listed, err := r.EtcdHealthcheckStatuses(suite.ctx, tcp, machines)
		g.Expect(err).To(MatchError(ContainSubstring("foo-b")))

		// Each unreachable machine times out after 5 seconds: one after the other, they would take 10 seconds.
		g.Expect(time.Since(start)).To(BeNumerically("<", 9*time.Second))

		// What was observed on each machine is kept for remediation.
		g.Expect(healthy).To(Equal(map[string]bool{"foo-a": true, "foo-b": false, "foo-c": false}))
		g.Expect(listed).To(Equal(map[string]int{"foo-a": 3, "foo-b": 0, "foo-c": 0}))
	})

	suite.Run("a learner doesn't count towards quorum", func() {
		g := NewWithT(suite.T())

		fakeClient := newFakeClient()
		cluster, tcp, _ := suite.setupCluster(fakeClient, "test-etcd-health-learner", pointer.Int32(2))

		msA, addrA := suite.startMachineServer()
		msB, addrB := suite.startMachineServer()

		g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

		// foo-b just joined as a learner and its etcd service is not healthy yet.
		members := []*machine.EtcdMember{
			{Id: 1, Hostname: "foo-a"},
			{Id: 2, Hostname: "foo-b", IsLearner: true},
		}

		machineA, _ := createMachineNodePair("foo-a", cluster, tcp, true, addrA)
		machineB, _ := createMachineNodePair("foo-b", cluster, tcp, true, addrB)

		suite.setEtcdRunning(addrA)
		msA.setEtcdMembersResponse(etcdMembers("foo-a", members...))

		msB.setServiceListResponse(&machine.ServiceListResponse{
			Messages: []*machine.ServiceList{
				{
					Metadata: &common.Metadata{Hostname: addrB},
					Services: []*machine.ServiceInfo{
						{
							Id: "etcd",
							Events: &machine.ServiceEvents{
								Events: []*machine.ServiceEvent{{Msg: "starting", State: "Preparing"}},
							},
						},
					},
				},
			},
		})
		msB.setEtcdMembersResponse(etcdMembers("foo-b", members...))

		r := newReconciler(fakeClient)

		reason, err := r.EtcdHealthcheckReason(suite.ctx, tcp, []clusterv1.Machine{*machineA, *machineB})
		g.Expect(err).To(MatchError(ContainSubstring("foo-b")))
		g.Expect(reason).To(Equal(controlplanev1.EtcdClusterUnhealthyReason))
	})

	suite.Run("the member of a machine being deleted may still keep quorum", func() {
		g := NewWithT(suite.T())

		fakeClient := newFakeClient()
		cluster, tcp, _ := suite.setupCluster(fakeClient, "test-etcd-health-deleting", pointer.Int32(3))

		_, addrA := suite.startMachineServer()
		msB, addrB := suite.startMachineServer()
		msC, addrC := suite.startMachineServer()

		g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrB)).To(Succeed())

		members := []*machine.EtcdMember{
			{Id: 1, Hostname: "foo-a"},
			{Id: 2, Hostname: "foo-b"},
			{Id: 3, Hostname: "foo-c"},
		}

		// foo-a is being deleted, so it is not inspected, but its etcd member still runs.
		machineA, _ := createMachineNodePair("foo-a", cluster, tcp, true, addrA)
		machineA.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		machineB, _ := createMachineNodePair("foo-b", cluster, tcp, true, addrB)
		machineC, _ := createMachineNodePair("foo-c", cluster, tcp, true, addrC)

		suite.setEtcdRunning(addrB)
		msB.setEtcdMembersResponse(etcdMembers("foo-b", members...))

		msC.setServiceListResponse(&machine.ServiceListResponse{
			Messages: []*machine.ServiceList{
				{
					Metadata: &common.Metadata{Hostname: addrC},
					Services: []*machine.ServiceInfo{
						{
							Id: "etcd",
							Events: &machine.ServiceEvents{
								Events: []*machine.ServiceEvent{{Msg: "crashed", State: "Failed"}},
							},
						},
					},
				},
			},
		})

		r := newReconciler(fakeClient)

		reason, err := r.EtcdHealthcheckReason(suite.ctx, tcp, []clusterv1.Machine{*machineA, *machineB, *machineC})
		g.Expect(err).To(MatchError(ContainSubstring("foo-c")))
		g.Expect(reason).To(Equal(controlplanev1.EtcdClusterInspectionFailedReason))
	})
}
