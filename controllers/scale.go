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

	return r.bootControlPlane(ctx, cluster, tcp, controlPlane, false)
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

	// Remediation deletes at most one Machine at a time: don't delete another one before its replacement is up.
	if remediationInProgress(tcp) {
		conditions.Set(tcp, metav1.Condition{
			Type:    string(controlplanev1.ResizedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.ScalingDownReason,
			Message: "Waiting for the remediation of an unhealthy machine to complete before scaling down",
		})

		r.Log.Info("waiting for remediation to complete before scaling down")

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// A Machine marked as leaving etcd is deleted, or counts again, once reconcileEtcdLeavingMachines could check
	// whether its member left etcd: don't delete another one before.
	for _, machine := range controlPlane.Machines {
		if machine.DeletionTimestamp.IsZero() && machine.Annotations[etcdLeavingAnnotation] == "true" {
			conditions.Set(tcp, metav1.Condition{
				Type:    string(controlplanev1.ResizedCondition),
				Status:  metav1.ConditionFalse,
				Reason:  controlplanev1.ScalingDownReason,
				Message: fmt.Sprintf("Waiting for machine %s to leave etcd before scaling down", machine.Name),
			})

			r.Log.Info("waiting for a machine to leave etcd before scaling down", "machine", machine.Name)

			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}

	// Machines being deleted, leaving etcd or flagged as unhealthy are on their way out and may never finish booting
	// again (e.g. after a reset): waiting for them would keep the scale down from deleting them.
	bootingMachines := controlPlane.Machines.Filter(collections.Not(collections.IsUnhealthyAndOwnerRemediated))
	if err := r.ensureNodesBooted(ctx, controlPlane.TCP, remainingMachines(collections.ToMachineList(bootingMachines).Items)); err != nil {
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

	deleteMachine, err := selectMachineForScaleDown(ctx, controlPlane, machinesRequireUpgrade)
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

	node := deleteMachine.Status.NodeRef

	r.Log.Info("deleting machine", "machine", deleteMachine.Name, "node", node.Name)

	if err := r.removeFromEtcdAndDelete(ctx, tcp, deleteMachine, controlPlane.Machines.UnsortedList()); err != nil {
		return ctrl.Result{}, err
	}

	result, err := r.deleteNode(ctx, client, deleteMachine)
	if err != nil {
		return result, err
	}

	if result.IsZero() {
		result.RequeueAfter = 5 * time.Second
	}

	return result, nil
}

// removeFromEtcdAndDelete removes the etcd member of the Machine, then deletes the Machine. If the member could not be
// removed, the Machine is kept, so that it is retried later rather than deleting a Machine whose member is still part
// of the etcd cluster, and the Resized condition reports why.
func (r *TalosControlPlaneReconciler) removeFromEtcdAndDelete(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machine *clusterv1.Machine, machines []*clusterv1.Machine) error {
	if err := r.removeMachineFromEtcd(ctx, tcp, machine, machines); err != nil {
		conditions.Set(tcp, metav1.Condition{
			Type:    string(controlplanev1.ResizedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.ScalingDownReason,
			Message: fmt.Sprintf("Failed to remove machine %s from etcd: %v", machine.Name, err),
		})

		return err
	}

	return r.Client.Delete(ctx, machine)
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

// selectMachineForScaleDown picks the machine to remove: one the user annotated for deletion first,
// then one a MachineHealthCheck flagged for remediation (removing anything else while that member is
// down could cost etcd its quorum), then an outdated one, and any machine otherwise. Among the
// candidates left, the one in the failure domain holding the most machines is removed.
func selectMachineForScaleDown(ctx context.Context, controlPlane *ControlPlane, outdatedMachines collections.Machines) (*clusterv1.Machine, error) {
	machines := controlPlane.Machines
	switch {
	case controlPlane.MachineWithDeleteAnnotation(outdatedMachines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(outdatedMachines)
	case controlPlane.MachineWithDeleteAnnotation(machines).Len() > 0:
		machines = controlPlane.MachineWithDeleteAnnotation(machines)
	case outdatedMachines.Filter(collections.IsUnhealthyAndOwnerRemediated).Len() > 0:
		machines = outdatedMachines.Filter(collections.IsUnhealthyAndOwnerRemediated)
	case controlPlane.UnhealthyMachines().Len() > 0:
		machines = controlPlane.UnhealthyMachines()
	case outdatedMachines.Len() > 0:
		machines = outdatedMachines
	}

	return controlPlane.MachineInFailureDomainWithMostMachines(ctx, machines)
}
