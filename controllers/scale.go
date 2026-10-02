// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const etcdLeavingAnnotation = "controlplane.cluster.x-k8s.io/etcd-leaving"

func (r *TalosControlPlaneReconciler) scaleUpControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane) (ctrl.Result, error) {
	numMachines := len(controlPlane.Machines)
	desiredReplicas := tcp.Spec.GetReplicas()

	conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingUpReason, clusterv1.ConditionSeverityWarning,
		"Scaling up control plane to %d replicas (actual %d)",
		desiredReplicas, numMachines)

	// Create a new Machine w/ join
	r.Log.Info("scaling up control plane", "Desired", desiredReplicas, "Existing", numMachines)

	return r.bootControlPlane(ctx, cluster, tcp, false)
}

func (r *TalosControlPlaneReconciler) scaleDownControlPlane(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	controlPlane *ControlPlane,
	machinesRequireUpgrade collections.Machines) (ctrl.Result, error) {

	numMachines := len(controlPlane.Machines)
	desiredReplicas := tcp.Spec.GetReplicas()

	if unhealthy := controlPlane.UnhealthyMachines(); unhealthy.Len() > 0 {
		conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingDownReason, clusterv1.ConditionSeverityWarning,
			"Remediating unhealthy control plane machines %v (%d replicas, %d desired)",
			unhealthy.Names(), numMachines, desiredReplicas)
	} else {
		conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingDownReason, clusterv1.ConditionSeverityWarning,
			"Scaling down control plane to %d replicas (actual %d)",
			desiredReplicas, numMachines)
	}

	if numMachines == 1 {
		conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingDownReason, clusterv1.ConditionSeverityError,
			"Cannot scale down control plane nodes to 0")

		return ctrl.Result{}, nil
	}

	if numMachines == 0 {
		return ctrl.Result{}, fmt.Errorf("no machines found")
	}

	// a machine on its way out (deleting, leaving etcd, or flagged for remediation) may never finish
	// booting, or be unreachable altogether; waiting for it would block the scale-down that removes it
	if err := r.ensureNodesBooted(ctx, controlPlane.TCP, remainingMachines(collections.ToMachineList(controlPlane.Machines).Items)); err != nil {
		r.Log.Info("waiting for all nodes to finish boot sequence", "error", err)

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if !conditions.IsTrue(tcp, controlplanev1.EtcdClusterHealthyCondition) {
		r.Log.Info("waiting for etcd to become healthy before scaling down")

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	r.Log.Info("scaling down control plane", "Desired", desiredReplicas, "Existing", numMachines)

	client, err := r.Tracker.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	deleteMachine, err := selectMachineForScaleDown(controlPlane, machinesRequireUpgrade)
	if err != nil {
		return ctrl.Result{}, err
	}

	waitForNodeRefs := false

	// iterate through the list of machines
	// delete nodes for the machines which are being destroyed
	for _, machine := range controlPlane.Machines {
		// do not allow scaling down until all nodes have nodeRefs, except for a machine flagged for
		// remediation: it may never get one (e.g. it failed to boot), and it is removed first anyway
		if machine.Status.NodeRef == nil {
			if needsRemediation(machine) {
				continue
			}

			r.Log.Info("one of machines does not have NodeRef", "machine", machine.Name)

			waitForNodeRefs = true

			continue
		}

		if !machine.ObjectMeta.DeletionTimestamp.IsZero() {
			r.Log.Info("machine is in process of deletion", "machine", machine.Name)

			return r.deleteNode(ctx, client, machine)
		}
	}

	if waitForNodeRefs {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	r.Log.Info("deleting machine", "machine", deleteMachine.Name, "node", nodeName(deleteMachine))

	// Mark machine as leaving etcd so health check skips it even if reconciliation
	// crashes between gracefulEtcdLeave and Client.Delete (prevents deadlock where
	// stopped etcd without DeletionTimestamp fails health checks forever).
	patchHelper, err := patch.NewHelper(deleteMachine, r.Client)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	annotations := deleteMachine.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[etcdLeavingAnnotation] = "true"
	deleteMachine.SetAnnotations(annotations)

	if needsRemediation(deleteMachine) {
		// surface to the MachineHealthCheck controller that the owner picked the machine up for remediation
		conditions.MarkFalse(deleteMachine, clusterv1.MachineOwnerRemediatedCondition, clusterv1.RemediationInProgressReason, clusterv1.ConditionSeverityWarning, "")
	}

	if err := patchHelper.Patch(ctx, deleteMachine); err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	// A machine without a noderef is only selected when it is flagged for remediation. It never
	// registered a node, so there is nothing to drain or delete in the workload cluster, and it
	// might not even be reachable to leave etcd gracefully: if it did join etcd, auditEtcd removes
	// the member as an orphan once the machine is gone.
	if deleteMachine.Status.NodeRef == nil {
		if err := r.Client.Delete(ctx, deleteMachine); err != nil {
			return ctrl.Result{}, err
		}

		return ctrl.Result{Requeue: true}, nil
	}

	c, err := r.talosconfigForMachines(ctx, tcp, *deleteMachine)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	defer c.Close() //nolint:errcheck

	leaveErr := r.gracefulEtcdLeave(ctx, c, *deleteMachine)

	err = r.Client.Delete(ctx, deleteMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	if leaveErr != nil {
		// the machine is gone either way; auditEtcd removes its member as an orphan if it is still listed
		r.Log.Info("failed to leave etcd gracefully", "machine", deleteMachine.Name, "error", leaveErr)

		return ctrl.Result{Requeue: true}, nil
	}

	result, err := r.deleteNode(ctx, client, deleteMachine)
	if err != nil {
		return result, err
	}

	result.Requeue = true

	return result, nil
}

// nodeName returns the name of the node the machine registered, or an empty string if it has none yet.
func nodeName(machine *clusterv1.Machine) string {
	if machine.Status.NodeRef == nil {
		return ""
	}

	return machine.Status.NodeRef.Name
}

func (r *TalosControlPlaneReconciler) deleteNode(ctx context.Context, client client.Client, machine *clusterv1.Machine) (ctrl.Result, error) {
	var node v1.Node

	name := types.NamespacedName{Name: machine.Status.NodeRef.Name, Namespace: machine.Status.NodeRef.Namespace}

	err := client.Get(ctx, name, &node)
	if err != nil {
		// It's possible for the node to already be deleted in the workload cluster, so we just
		// requeue if that's that case instead of throwing a scary error.
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
		}

		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	r.Log.Info("deleting node", "machine", machine.Name, "node", node.Name)

	err = client.Delete(ctx, &node)
	if err != nil {
		r.Log.Error(err, "failed to delete the node", "machine", machine.Name, "node", node.Name)

		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// selectMachineForScaleDown picks the machine to remove: one the user annotated for deletion first,
// then one a MachineHealthCheck flagged for remediation (removing anything else while that member is
// down could cost etcd its quorum), then an outdated one, and the oldest machine otherwise.
func selectMachineForScaleDown(controlPlane *ControlPlane, outdatedMachines collections.Machines) (*clusterv1.Machine, error) {
	machines := controlPlane.Machines
	switch {
	case controlPlane.MachineWithDeleteAnnotation(outdatedMachines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(outdatedMachines)
	case controlPlane.MachineWithDeleteAnnotation(machines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(machines)
	case outdatedMachines.Filter(needsRemediation).Len() > 0:
		machines = outdatedMachines.Filter(needsRemediation)
	case controlPlane.UnhealthyMachines().Len() > 0:
		machines = controlPlane.UnhealthyMachines()
	case outdatedMachines.Len() > 0:
		machines = outdatedMachines
	}

	return machines.Oldest(), nil
}
