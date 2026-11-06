// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"fmt"

	. "github.com/onsi/gomega"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/controllers"
)

// TestAuditEtcdMatchesMembersByID checks that auditEtcd identifies the etcd member of a machine by the member ID the
// machine reports, so that a member that joined etcd under a temporary hostname is not removed (#278).
func (suite *ControllersSuite) TestAuditEtcdMatchesMembersByID() {
	for i, tc := range []struct {
		name string
		// idA and idB are the member IDs foo-a and foo-b report, 0 if they report none
		idA, idB   uint64
		members    []*machine.EtcdMember
		wantRemove []uint64
	}{
		{
			name: "a member that joined under a temporary hostname is kept, an orphan is removed",
			idA:  1,
			idB:  100,
			members: []*machine.EtcdMember{
				{Id: 1, Hostname: "foo-a"},
				{Id: 100, Hostname: "talos-4y4-bo8"},
				{Id: 7, Hostname: "talos-dtq-420"},
			},
			wantRemove: []uint64{7},
		},
		{
			name: "a member with the host name of a machine that runs another member is removed",
			idA:  1,
			idB:  100,
			members: []*machine.EtcdMember{
				{Id: 1, Hostname: "foo-a"},
				{Id: 5, Hostname: "foo-b"},
				{Id: 100, Hostname: "talos-4y4-bo8"},
			},
			wantRemove: []uint64{5},
		},
		{
			name: "machines without a member ID are matched by host name",
			members: []*machine.EtcdMember{
				{Id: 1, Hostname: "foo-a"},
				{Id: 2, Hostname: "foo-b"},
				{Id: 7, Hostname: "talos-dtq-420"},
			},
			wantRemove: []uint64{7},
		},
		{
			// foo-b doesn't report its member ID, e.g. because its Talos version has no etcd status API
			name: "no member is removed while the member of a machine can't be identified",
			idA:  1,
			members: []*machine.EtcdMember{
				{Id: 1, Hostname: "foo-a"},
				{Id: 100, Hostname: "talos-4y4-bo8"},
			},
		},
	} {
		suite.Run(tc.name, func() {
			g := NewWithT(suite.T())

			fakeClient := newFakeClient()
			cluster, tcp, _ := suite.setupCluster(fakeClient, fmt.Sprintf("test-etcd-audit-id-%d", i), ptr.To[int32](2))

			msA, addrA := suite.startMachineServer()
			msB, addrB := suite.startMachineServer()

			g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

			for _, ms := range []*machineService{msA, msB} {
				ms.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
					Messages: []*machine.EtcdMembers{{Metadata: &common.Metadata{Hostname: "foo"}, Members: tc.members}},
				})
			}

			msA.setEtcdMemberID(tc.idA)
			msB.setEtcdMemberID(tc.idB)

			suite.createMachines(fakeClient, cluster, tcp, addrA, addrB)

			r := newReconciler(fakeClient)

			g.Expect(r.AuditEtcd(suite.ctx, tcp, util.ObjectKey(cluster))).To(Succeed())

			removed := []uint64{}

			for _, ms := range []*machineService{msA, msB} {
				for id := range ms.getEtcdRemoveMemberRequests() {
					removed = append(removed, id)
				}
			}

			g.Expect(removed).To(ConsistOf(tc.wantRemove))
		})
	}
}

// TestEtcdMemberIDIsRecorded checks that the etcd member ID each machine reports to the etcd health check is recorded
// on the machine.
func (suite *ControllersSuite) TestEtcdMemberIDIsRecorded() {
	g := NewWithT(suite.T())

	fakeClient := newFakeClient()
	cluster, tcp, _ := suite.setupCluster(fakeClient, "test-etcd-member-id-recorded", ptr.To[int32](2))

	msA, addrA := suite.startMachineServer()
	msB, addrB := suite.startMachineServer()

	g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

	// foo-b joined etcd under a temporary hostname; foo-a doesn't report its member ID
	members := []*machine.EtcdMember{{Id: 1, Hostname: "foo-a"}, {Id: 0x64, Hostname: "talos-4y4-bo8"}}

	for _, m := range []struct {
		name    string
		address string
		ms      *machineService
	}{{"foo-a", addrA, msA}, {"foo-b", addrB, msB}} {
		suite.setEtcdRunning(m.address)
		m.ms.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
			Messages: []*machine.EtcdMembers{{Metadata: &common.Metadata{Hostname: m.name}, Members: members}},
		})
	}

	msB.setEtcdMemberID(0x64)

	machines := suite.createMachines(fakeClient, cluster, tcp, addrA, addrB)

	r := newReconciler(fakeClient)

	healthErr, recordErr := r.EtcdHealthcheckAndRecordMemberIDs(suite.ctx, tcp, machines)
	g.Expect(healthErr).NotTo(HaveOccurred())
	g.Expect(recordErr).NotTo(HaveOccurred())

	var fooA, fooB clusterv1.Machine

	g.Expect(fakeClient.Get(suite.ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: "foo-a"}, &fooA)).To(Succeed())
	g.Expect(fakeClient.Get(suite.ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: "foo-b"}, &fooB)).To(Succeed())

	g.Expect(fooA.Annotations).NotTo(HaveKey(controllers.EtcdMemberIDAnnotation))
	g.Expect(fooB.Annotations).To(HaveKeyWithValue(controllers.EtcdMemberIDAnnotation, "0000000000000064"))
}

// createMachines creates the control plane machines foo-a, foo-b, ... with a node, each at its address.
func (suite *ControllersSuite) createMachines(fakeClient client.Client, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, addresses ...string) []clusterv1.Machine {
	g := NewWithT(suite.T())

	machines := make([]clusterv1.Machine, 0, len(addresses))

	for i, address := range addresses {
		cpMachine, _ := createMachineNodePair(fmt.Sprintf("foo-%c", 'a'+i), cluster, tcp, true, address)
		status := cpMachine.Status

		g.Expect(fakeClient.Create(suite.ctx, cpMachine)).To(Succeed())

		cpMachine.Status = status
		g.Expect(fakeClient.Status().Update(suite.ctx, cpMachine)).To(Succeed())

		machines = append(machines, *cpMachine)
	}

	return machines
}
