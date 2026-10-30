// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// TestPatchRejectsConditionWithoutReason checks that a condition set without a reason fails the patch with an error
// that names it, instead of being stored with a placeholder reason.
func TestPatchRejectsConditionWithoutReason(t *testing.T) {
	g := NewWithT(t)

	e := newRemediationEnv(t, newTestTCP(1))

	patchHelper, err := patch.NewHelper(e.tcp, e.client)
	g.Expect(err).NotTo(HaveOccurred())

	conditions.Set(e.tcp, metav1.Condition{
		Type:   string(controlplanev1.ResizedCondition),
		Status: metav1.ConditionTrue,
	})

	err = patchTalosControlPlane(e.ctx, patchHelper, e.tcp)
	g.Expect(err).To(MatchError(ContainSubstring(`condition "Resized" of the TalosControlPlane was set without a reason`)))

	stored := &controlplanev1.TalosControlPlane{}
	g.Expect(e.client.Get(e.ctx, client.ObjectKeyFromObject(e.tcp), stored)).To(Succeed())
	g.Expect(conditions.Has(stored, string(controlplanev1.ResizedCondition))).To(BeFalse())
}
