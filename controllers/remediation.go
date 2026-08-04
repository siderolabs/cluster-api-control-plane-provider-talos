// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const (
	// remediationInProgressAnnotation is set on TalosControlPlane while a machine
	// replacement is in flight. Its value is the name of the deleted machine.
	// Persisted across controller restarts; prevents concurrent remediations.
	remediationInProgressAnnotation = "controlplane.cluster.x-k8s.io/remediation-in-progress"
)

// reconcileUnhealthyMachines looks for control plane Machines that have been
// marked for remediation by a MachineHealthCheck (OwnerRemediated condition set
// to False) and deletes them so that CACPPT's normal scale-up logic recreates a
// replacement.
//
// Safety invariants (mirrors KubeadmControlPlane):
//  1. Cluster must be initialized (tcp.Status.Initialized == true).
//  2. At least 2 control plane machines must exist (minimum for etcd quorum).
//  3. No other remediation in progress (remediationInProgressAnnotation absent).
//  4. No machine currently deleting (DeletionTimestamp set).
//  5. At most one machine deleted per reconcile pass.
func (r *TalosControlPlaneReconciler) reconcileUnhealthyMachines(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	cluster *clusterv1.Cluster,
	machines []clusterv1.Machine,
) (ctrl.Result, error) {
	log := r.Log.WithValues(
		"TalosControlPlane", fmt.Sprintf("%s/%s", tcp.Namespace, tcp.Name),
		"Cluster", cluster.Name,
	)

	// Guard 1 — cluster initialized
	if !tcp.Status.Initialized {
		log.V(4).Info("cluster not yet initialized, skipping remediation")
		return ctrl.Result{}, nil
	}

	// Guard 2 — minimum replica count
	if len(machines) < 2 {
		log.Info("fewer than 2 control plane machines, remediation unsafe — skipping",
			"machineCount", len(machines))
		return ctrl.Result{}, nil
	}

	// Guard 3 — no remediation already in progress
	if _, inProgress := tcp.Annotations[remediationInProgressAnnotation]; inProgress {
		log.V(4).Info("remediation already in progress, checking if replacement is Ready")
		if err := r.clearRemediationInProgressIfDone(ctx, tcp, machines, log); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Guard 4 — no machine currently deleting
	for _, m := range machines {
		if !m.DeletionTimestamp.IsZero() {
			log.V(4).Info("a machine is already being deleted, skipping remediation",
				"machine", m.Name)
			return ctrl.Result{}, nil
		}
	}

	// Find the first machine with OwnerRemediated=False (set by MachineHealthCheck).
	//
	// CAPI v1beta2: the MHC controller sets two parallel conditions:
	//   • metav1.Condition  "OwnerRemediated" (Status=False) via apimeta  ← we check this one
	//   • legacy clusterv1.Condition "OwnerRemediated" (Status=False)     ← also checked as fallback
	//
	// Constants (from sigs.k8s.io/cluster-api/api/core/v1beta2):
	//   clusterv1.MachineOwnerRemediatedCondition = "OwnerRemediated"
	var target *clusterv1.Machine
	for i := range machines {
		m := &machines[i]

		// Primary check: metav1.Condition on machine.Status.Conditions
		if c := apimeta.FindStatusCondition(m.Status.Conditions, clusterv1.MachineOwnerRemediatedCondition); c != nil {
			if c.Status == metav1.ConditionFalse {
				target = m
				break
			}
		}

		// Fallback: legacy clusterv1.Condition (v1beta1 compat layer still set by MHC)
		if target == nil {
			if conditions.IsFalse(m, clusterv1.MachineOwnerRemediatedCondition) {
				target = m
				break
			}
		}
	}

	if target == nil {
		return ctrl.Result{}, nil
	}

	log.Info("remediating unhealthy control plane machine",
		"machine", target.Name)

	// Persist the in-progress annotation before deleting, so that a controller
	// restart doesn't lose track of the ongoing remediation.
	tcpPatch := client.MergeFrom(tcp.DeepCopy())
	if tcp.Annotations == nil {
		tcp.Annotations = map[string]string{}
	}
	tcp.Annotations[remediationInProgressAnnotation] = target.Name
	if err := r.Client.Patch(ctx, tcp, tcpPatch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set remediation-in-progress annotation: %w", err)
	}

	// Delete the unhealthy machine. CACPPT's reconcileMachines will detect
	// replicas < desired on the next pass and create a replacement.
	if err := r.Client.Delete(ctx, target); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete unhealthy machine %s: %w", target.Name, err)
	}

	log.Info("deleted unhealthy machine, replacement will be created on next reconcile",
		"machine", target.Name)

	// Requeue quickly so the replacement is created without waiting for the
	// default requeueDuration.
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// clearRemediationInProgressIfDone removes the remediationInProgressAnnotation
// from the TalosControlPlane once all desired replicas (minus the deleted one)
// are Ready — meaning a replacement has joined and is healthy.
func (r *TalosControlPlaneReconciler) clearRemediationInProgressIfDone(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	machines []clusterv1.Machine,
	log logr.Logger,
) error {
	deletedMachineName := tcp.Annotations[remediationInProgressAnnotation]
	desired := tcp.Spec.GetReplicas()

	readyCount := int32(0)
	for _, m := range machines {
		// Skip the machine being replaced (may still appear with a DeletionTimestamp)
		if m.Name == deletedMachineName {
			continue
		}
		if m.DeletionTimestamp.IsZero() && conditions.IsTrue(&m, clusterv1.ReadyCondition) {
			readyCount++
		}
	}

	// All desired replicas (after replacement) are Ready → remediation complete.
	if readyCount >= desired {
		log.Info("remediation complete, replacement machine is Ready — clearing annotation",
			"deletedMachine", deletedMachineName, "readyReplicas", readyCount)
		tcpPatch := client.MergeFrom(tcp.DeepCopy())
		delete(tcp.Annotations, remediationInProgressAnnotation)
		if err := r.Client.Patch(ctx, tcp, tcpPatch); err != nil {
			return fmt.Errorf("failed to clear remediation-in-progress annotation: %w", err)
		}
	}

	return nil
}
