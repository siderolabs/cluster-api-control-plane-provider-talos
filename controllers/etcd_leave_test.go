// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"fmt"

	. "github.com/onsi/gomega"
	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// TestRemoveMachineFromEtcd checks that a machine only leaves etcd by itself while its etcd service runs, and that
// otherwise its member is checked, and removed, through another machine.
func (suite *ControllersSuite) TestRemoveMachineFromEtcd() {
	for i, tc := range []struct {
		name       string
		state      string
		memberOfB  bool
		wantLeave  int
		wantRemove bool
	}{
		{name: "a running member leaves by itself", state: "Running", memberOfB: true, wantLeave: 1},
		{name: "a stopped member is removed through another machine", state: "Finished", memberOfB: true, wantRemove: true},
		{name: "a failed member is removed through another machine", state: "Failed", memberOfB: true, wantRemove: true},
		{name: "a stopped machine that already left etcd is not removed again", state: "Finished"},
	} {
		suite.Run(tc.name, func() {
			g := NewWithT(suite.T())

			fakeClient := newFakeClient()
			cluster, tcp, _ := suite.setupCluster(fakeClient, fmt.Sprintf("test-etcd-leave-%d", i), pointer.Int32(2))

			msA, addrA := suite.startMachineServer()
			msB, addrB := suite.startMachineServer()

			g.Expect(createSecrets(suite.ctx, fakeClient, cluster, suite.secretsBundle, addrA)).To(Succeed())

			msA.setServiceListResponse(&machine.ServiceListResponse{
				Messages: []*machine.ServiceList{
					{
						Metadata: &common.Metadata{Hostname: addrA},
						Services: []*machine.ServiceInfo{{Id: "etcd", State: tc.state}},
					},
				},
			})

			members := []*machine.EtcdMember{{Id: 2, Hostname: "foo-b"}}
			if tc.memberOfB {
				members = append(members, &machine.EtcdMember{Id: 1, Hostname: "foo-a"})
			}

			msB.setEtcdMembersResponse(&machine.EtcdMemberListResponse{
				Messages: []*machine.EtcdMembers{{Metadata: &common.Metadata{Hostname: "foo-b"}, Members: members}},
			})

			var machines []clusterv1.Machine

			for _, m := range []struct{ name, address string }{{"foo-a", addrA}, {"foo-b", addrB}} {
				cpMachine, _ := createMachineNodePair(m.name, cluster, tcp, true, m.address)
				status := cpMachine.Status

				g.Expect(fakeClient.Create(suite.ctx, cpMachine)).To(Succeed())

				cpMachine.Status = status
				g.Expect(fakeClient.Status().Update(suite.ctx, cpMachine)).To(Succeed())

				machines = append(machines, *cpMachine)
			}

			r := newReconciler(fakeClient)

			g.Expect(r.RemoveMachineFromEtcd(suite.ctx, tcp, &machines[0], machines)).To(Succeed())

			g.Expect(msA.getEtcdLeaveClusterRequests()).To(Equal(tc.wantLeave))
			g.Expect(msA.getEtcdRemoveMemberRequests()).To(BeEmpty())

			if tc.wantRemove {
				g.Expect(msB.getEtcdRemoveMemberRequests()).To(HaveKey(uint64(1)))
			} else {
				g.Expect(msB.getEtcdRemoveMemberRequests()).To(BeEmpty())
			}
		})
	}
}
