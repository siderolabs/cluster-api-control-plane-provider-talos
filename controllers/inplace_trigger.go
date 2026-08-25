// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/internal/hooks"
	"github.com/siderolabs/cluster-api-control-plane-provider-talos/internal/ssa"
)

// triggerInPlaceUpdate commits a Machine to being updated in place.
//
// The ordering matters and mirrors the KubeadmControlPlane implementation:
//
//  1. Stamp UpdateInProgressAnnotation on the Machine. From this point Cluster API is
//     committed to the in-place path; there is no falling back to a rolling replacement,
//     which is why every subsequent failure has to surface rather than be swallowed.
//  2. Write the desired InfraMachine and TalosConfig, each carrying the same annotation.
//     The core Machine controller waits for it on all three objects before it will call the
//     UpdateMachine hook.
//  3. Mark the UpdateMachine hook pending, which releases the Machine controller to start.
//
// The annotations are removed by the core Machine controller once the update completes.
func (r *TalosControlPlaneReconciler) triggerInPlaceUpdate(ctx context.Context, plan *inPlacePlan) error {
	log := r.Log.WithValues("machine", klog.KObj(plan.machine))

	if _, ok := plan.machine.Annotations[clusterv1.UpdateInProgressAnnotation]; !ok {
		orig := plan.machine.DeepCopy()

		if plan.machine.Annotations == nil {
			plan.machine.Annotations = map[string]string{}
		}

		plan.machine.Annotations[clusterv1.UpdateInProgressAnnotation] = ""

		// Deliberately a merge patch rather than server-side apply: the core Machine
		// controller removes this annotation when the update finishes, and an apply would
		// keep putting it back.
		if err := r.Client.Patch(ctx, plan.machine, client.MergeFrom(orig)); err != nil {
			return fmt.Errorf("failed to mark machine %s for in-place update: %w", klog.KObj(plan.machine), err)
		}
	}

	// The InfraMachine is written first because it is the call most likely to fail.
	if plan.desiredInfraMachine != nil {
		desired := plan.desiredInfraMachine.DeepCopy()
		// Server-side apply is declarative and must not carry a resourceVersion; these
		// desired objects are copies of live ones, so it has to be cleared or the apply
		// conflicts with the annotation patch written above.
		desired.SetResourceVersion("")
		desired.SetManagedFields(nil)
		desired.SetLabels(nil)
		desired.SetAnnotations(map[string]string{
			clusterv1.TemplateClonedFromNameAnnotation:      plan.desiredInfraMachine.GetAnnotations()[clusterv1.TemplateClonedFromNameAnnotation],
			clusterv1.TemplateClonedFromGroupKindAnnotation: plan.desiredInfraMachine.GetAnnotations()[clusterv1.TemplateClonedFromGroupKindAnnotation],
			clusterv1.UpdateInProgressAnnotation:            "",
		})

		if err := ssa.Patch(ctx, r.Client, desired); err != nil {
			return fmt.Errorf("failed to write desired InfraMachine for %s: %w", klog.KObj(plan.machine), err)
		}
	}

	if plan.desiredTalosConfig != nil {
		desired := plan.desiredTalosConfig.DeepCopy()
		// Server-side apply refuses an object without apiVersion/kind, and the typed client
		// strips TypeMeta when it reads one, so it has to be restored here.
		desired.TypeMeta = metav1.TypeMeta{
			APIVersion: cabptv1.GroupVersion.String(),
			Kind:       "TalosConfig",
		}
		desired.ResourceVersion = ""
		desired.ManagedFields = nil
		desired.Labels = nil
		desired.Annotations = map[string]string{
			// CABPT's validating webhook admits a spec change only when this annotation is
			// present, and regenerates the bootstrap data secret because of it.
			clusterv1.UpdateInProgressAnnotation: "",
		}

		if err := ssa.Patch(ctx, r.Client, desired); err != nil {
			return fmt.Errorf("failed to write desired TalosConfig for %s: %w", klog.KObj(plan.machine), err)
		}
	}

	if plan.desiredMachine != nil {
		plan.desiredMachine.TypeMeta = metav1.TypeMeta{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Machine",
		}
		plan.desiredMachine.ResourceVersion = ""
		plan.desiredMachine.ManagedFields = nil

		if err := ssa.Patch(ctx, r.Client, plan.desiredMachine); err != nil {
			return fmt.Errorf("failed to write desired Machine for %s: %w", klog.KObj(plan.machine), err)
		}
	}

	if err := hooks.MarkAsPending(ctx, r.Client, plan.machine, runtimehooksv1.UpdateMachine); err != nil {
		return fmt.Errorf("failed to start in-place update for %s: %w", klog.KObj(plan.machine), err)
	}

	log.Info("triggered in-place update")

	return nil
}
