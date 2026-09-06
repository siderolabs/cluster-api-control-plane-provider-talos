// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"time"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const etcdLeavingAnnotation = "controlplane.cluster.x-k8s.io/etcd-leaving"

func (r *TalosControlPlaneReconciler) scaleUpControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane) (ctrl.Result, error) {
	numMachines := len(controlPlane.Machines)
	desiredReplicas := tcp.Spec.GetReplicas()

	conditions.Set(tcp, metav1.Condition{
		Type:    string(controlplanev1.ResizedCondition),
		Status:  metav1.ConditionFalse,
		Reason:  controlplanev1.ScalingUpReason,
		Message: fmt.Sprintf("Scaling up control plane to %d replicas (actual %d)", desiredReplicas, numMachines),
	})

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

	conditions.Set(tcp, metav1.Condition{
		Type:    string(controlplanev1.ResizedCondition),
		Status:  metav1.ConditionFalse,
		Reason:  controlplanev1.ScalingDownReason,
		Message: fmt.Sprintf("Scaling down control plane to %d replicas (actual %d)", desiredReplicas, numMachines),
	})

	if numMachines == 1 {
		conditions.Set(tcp, metav1.Condition{
			Type:    string(controlplanev1.ResizedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.ScalingDownReason,
			Message: "Cannot scale down control plane nodes to 0",
		})

		return ctrl.Result{}, nil
	}

	if numMachines == 0 {
		return ctrl.Result{}, fmt.Errorf("no machines found")
	}

	if err := r.ensureNodesBooted(ctx, controlPlane.TCP, collections.ToMachineList(controlPlane.Machines).Items); err != nil {
		r.Log.Info("waiting for all nodes to finish boot sequence", "error", err)

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if !conditions.IsTrue(tcp, string(controlplanev1.EtcdClusterHealthyCondition)) {
		r.Log.Info("waiting for etcd to become healthy before scaling down")

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	r.Log.Info("scaling down control plane", "Desired", desiredReplicas, "Existing", numMachines)

	client, err := r.ClusterCache.GetClient(ctx, util.ObjectKey(cluster))
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
		// do not allow scaling down until all nodes have nodeRefs
		if !machine.Status.NodeRef.IsDefined() {
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

	return r.deleteControlPlaneMachine(ctx, client, tcp, deleteMachine)
}

// deleteControlPlaneMachine removes a control plane machine from etcd, where that is still this
// function's job, and deletes it.
func (r *TalosControlPlaneReconciler) deleteControlPlaneMachine(
	ctx context.Context,
	workloadClient client.Client,
	tcp *controlplanev1.TalosControlPlane,
	deleteMachine *clusterv1.Machine,
) (ctrl.Result, error) {
	node := deleteMachine.Status.NodeRef

	r.Log.Info("deleting machine", "machine", deleteMachine.Name, "node", node.Name)

	var leaveErr error

	// Machines carrying the pre-terminate hook have their etcd membership resolved by the
	// deletion pipeline (reconcileMachinePreTerminateHooks), which runs at the right point —
	// after drain and volume detach, before the infrastructure is torn down — instead of before
	// the deletion request. Deleting the Machine is all that is left to do for them.
	//
	// Everything else (the hook disabled, or a machine that predates it) still leaves etcd
	// inline, here, before the deletion request.
	if _, hooked := deleteMachine.Annotations[PreTerminateHookCleanupAnnotation]; !hooked {
		c, err := r.etcdClientFor(ctx, tcp, *deleteMachine)
		if err != nil {
			return ctrl.Result{RequeueAfter: 20 * time.Second}, err
		}

		defer c.Close() //nolint:errcheck

		// Mark machine as leaving etcd so health check skips it even if reconciliation
		// crashes between gracefulEtcdLeave and Client.Delete (prevents deadlock where
		// stopped etcd without DeletionTimestamp fails health checks forever).
		if err := r.markEtcdLeaving(ctx, deleteMachine); err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, err
		}

		leaveErr = r.gracefulEtcdLeave(ctx, c, *deleteMachine)
	}

	if err := r.Client.Delete(ctx, deleteMachine); err != nil {
		return ctrl.Result{}, err
	}

	// The machine is deleted either way — the etcd member is the part that failed — but the
	// failure is now reported instead of being swallowed, so the next reconcile retries rather
	// than leaving auditEtcd to discover the orphan on its own.
	if leaveErr != nil {
		return ctrl.Result{}, leaveErr
	}

	result, err := r.deleteNode(ctx, workloadClient, deleteMachine)
	if err != nil {
		return result, err
	}

	result.Requeue = true

	return result, nil
}

func (r *TalosControlPlaneReconciler) deleteNode(ctx context.Context, client client.Client, machine *clusterv1.Machine) (ctrl.Result, error) {
	var node v1.Node

	name := types.NamespacedName{Name: machine.Status.NodeRef.Name}

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

func selectMachineForScaleDown(controlPlane *ControlPlane, outdatedMachines collections.Machines) (*clusterv1.Machine, error) {
	machines := controlPlane.Machines
	switch {
	case controlPlane.MachineWithDeleteAnnotation(outdatedMachines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(outdatedMachines)
	case controlPlane.MachineWithDeleteAnnotation(machines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(machines)
	case outdatedMachines.Len() > 0:
		machines = outdatedMachines
	}

	return machines.Oldest(), nil
}
