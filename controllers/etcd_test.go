// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"

	. "github.com/onsi/gomega"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

func TestEtcdHealthConditionReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		health etcdHealth
		reason string
	}{
		{
			name:   "nothing inspected",
			health: etcdHealth{machines: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "one unhealthy member out of 3",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 2, members: 3},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "one unreachable machine out of 3",
			health: etcdHealth{machines: 3, inspected: 2, healthy: 2, members: 3},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "two unhealthy members out of 3",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
		{
			// The unreachable machines may well run healthy members.
			name:   "two unreachable machines out of 3",
			health: etcdHealth{machines: 3, inspected: 1, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "one unhealthy and one unreachable machine out of 3",
			health: etcdHealth{machines: 3, inspected: 2, healthy: 1, members: 3},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "two unhealthy members and one unreachable machine out of 5",
			health: etcdHealth{machines: 5, inspected: 4, healthy: 2, members: 5},
			reason: controlplanev1.EtcdClusterInspectionFailedReason,
		},
		{
			name:   "three unhealthy members out of 5",
			health: etcdHealth{machines: 5, inspected: 5, healthy: 2, members: 5},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
		{
			// A learner that is not healthy yet doesn't count towards quorum: 1 voting member, which is healthy.
			name:   "unhealthy learner joining a single member",
			health: etcdHealth{machines: 2, inspected: 2, healthy: 1, members: 1},
			reason: controlplanev1.EtcdClusterUnhealthyReason,
		},
		{
			name:   "no member list",
			health: etcdHealth{machines: 3, inspected: 3, healthy: 1},
			reason: controlplanev1.EtcdClusterQuorumLostReason,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			g.Expect(tc.health.conditionReason()).To(Equal(tc.reason))
		})
	}
}
