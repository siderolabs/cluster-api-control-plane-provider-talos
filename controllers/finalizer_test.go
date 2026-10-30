// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// TestEnsureFinalizersMigratesLegacyFinalizer checks that the legacy finalizer is replaced by the current one.
func TestEnsureFinalizersMigratesLegacyFinalizer(t *testing.T) {
	g := NewWithT(t)

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: []string{controlplanev1.TalosControlPlaneFinalizerLegacy},
		},
	}

	g.Expect(ensureTalosControlPlaneFinalizers(tcp)).To(BeTrue())
	g.Expect(tcp.Finalizers).To(ConsistOf(controlplanev1.TalosControlPlaneFinalizer))
}

// TestEnsureFinalizersSkipsDeletingObject checks that no finalizer is added to a TalosControlPlane being deleted,
// which the API server would reject, so that the deletion is reconciled instead.
func TestEnsureFinalizersSkipsDeletingObject(t *testing.T) {
	g := NewWithT(t)

	now := metav1.Now()
	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			DeletionTimestamp: &now,
			Finalizers:        []string{controlplanev1.TalosControlPlaneFinalizerLegacy},
		},
	}

	g.Expect(ensureTalosControlPlaneFinalizers(tcp)).To(BeFalse())
	g.Expect(tcp.Finalizers).To(ConsistOf(controlplanev1.TalosControlPlaneFinalizerLegacy))

	removeTalosControlPlaneFinalizers(tcp)
	g.Expect(tcp.Finalizers).To(BeEmpty())
}
