// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-semver/semver"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *TalosControlPlaneReconciler) scaleUpControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane) (ctrl.Result, error) {
	numMachines := len(controlPlane.Machines)
	desiredReplicas := tcp.Spec.GetReplicas()

	conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingUpReason, clusterv1.ConditionSeverityWarning,
		"Scaling up control plane to %d replicas (actual %d)",
		desiredReplicas, numMachines)

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

	conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingDownReason, clusterv1.ConditionSeverityWarning,
		"Scaling down control plane to %d replicas (actual %d)",
		desiredReplicas, numMachines)

	if numMachines == 1 {
		conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, controlplanev1.ScalingDownReason, clusterv1.ConditionSeverityError,
			"Cannot scale down control plane nodes to 0",
			desiredReplicas, numMachines)

		return ctrl.Result{}, nil
	}

	if numMachines == 0 {
		return ctrl.Result{}, fmt.Errorf("no machines found")
	}

	if err := r.ensureNodesBooted(ctx, controlPlane.TCP, cluster, collections.ToMachineList(controlPlane.Machines).Items); err != nil {
		r.Log.Info("waiting for all nodes to finish boot sequence", "error", err)

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if !conditions.IsTrue(tcp, controlplanev1.EtcdClusterHealthyCondition) {
		r.Log.Info("waiting for etcd to become healthy before scaling down")

		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	r.Log.Info("scaling down control plane", "Desired", desiredReplicas, "Existing", numMachines)

	r.Log.Info("Found control plane machines", "machines", numMachines)

	client, err := r.Tracker.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	deleteMachine, err := selectMachineForScaleDown(controlPlane, machinesRequireUpgrade)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, machine := range controlPlane.Machines {
		if !machine.ObjectMeta.DeletionTimestamp.IsZero() {
			r.Log.Info("machine is in process of deletion", "machine", machine.Name)

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

			r.Log.Info("Deleting node", "machine", machine.Name, "node", node.Name)

			err = client.Delete(ctx, &node)
			if err != nil {
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
		}

		// do not allow scaling down until all nodes have nodeRefs
		if machine.Status.NodeRef == nil {
			r.Log.Info("one of machines does not have NodeRef", "machine", machine.Name)

			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		if machine.CreationTimestamp.Before(&deleteMachine.CreationTimestamp) {
			deleteMachine = machine
		}
	}

	if deleteMachine.Status.NodeRef == nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, fmt.Errorf("%q machine does not have a nodeRef", deleteMachine.Name)
	}

	node := deleteMachine.Status.NodeRef

	c, err := r.talosconfigForMachines(ctx, tcp, *deleteMachine)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	defer c.Close() //nolint:errcheck

	err = r.gracefulEtcdLeave(ctx, c, util.ObjectKey(cluster), *deleteMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info("deleting machine", "machine", deleteMachine.Name, "node", node.Name)

	err = r.Client.Delete(ctx, deleteMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	// TODO: drop version check and shutdown when Talos < 0.12.2 reaches end of life
	version, err := c.Version(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	fromVersion, _ := semver.NewVersion("0.12.2") //nolint:errcheck

	nodeVersion, err := semver.NewVersion(
		strings.TrimLeft(version.Messages[0].Version.Tag, "v"),
	)

	if err != nil {
		return ctrl.Result{}, err
	}

	if nodeVersion.LessThan(*fromVersion) {
		// NB: We shutdown the node here so that a loadbalancer will drop the backend.
		// The Kubernetes API server is configured to talk to etcd on localhost, but
		// at this point etcd has been stopped.
		r.Log.Info("shutting down node", "machine", deleteMachine.Name, "node", node.Name)

		err = c.Shutdown(ctx)
		if err != nil {
			return ctrl.Result{RequeueAfter: 20 * time.Second}, err
		}
	}

	r.Log.Info("deleting node", "machine", deleteMachine.Name, "node", node.Name)

	n := &v1.Node{}
	n.Name = node.Name
	n.Namespace = node.Namespace

	err = client.Delete(ctx, n)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	// Requeue so that we handle any additional scaling.
	return ctrl.Result{Requeue: true}, nil
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
