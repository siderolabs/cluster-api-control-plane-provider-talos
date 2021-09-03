// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	cabptv1 "github.com/talos-systems/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	controlplanev1 "github.com/talos-systems/cluster-api-control-plane-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/utils/pointer"
	capiv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	"sigs.k8s.io/cluster-api/controllers/external"
	capierrors "sigs.k8s.io/cluster-api/errors"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type NodeType int

const (
	initNode NodeType = iota
	controlplaneNode
)

const requeueDuration = 30 * time.Second

// ControlPlane holds business logic around control planes.
// It should never need to connect to a service, that responsibility lies outside of this struct.
type ControlPlane struct {
	TCP      *controlplanev1.TalosControlPlane
	Cluster  *capiv1.Cluster
	Machines []capiv1.Machine
}

// TalosControlPlaneReconciler reconciles a TalosControlPlane object
type TalosControlPlaneReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

func (r *TalosControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1.TalosControlPlane{}).
		Owns(&capiv1.Machine{}).
		Watches(
			&source.Kind{Type: &capiv1.Cluster{}},
			&handler.EnqueueRequestsFromMapFunc{ToRequests: handler.ToRequestsFunc(r.ClusterToTalosControlPlane)},
		).
		WithOptions(options).
		Complete(r)
}

// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac,resources=roles,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac,resources=rolebindings,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io;bootstrap.cluster.x-k8s.io;controlplane.cluster.x-k8s.io,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch;create;update;patch;delete

func (r *TalosControlPlaneReconciler) Reconcile(req ctrl.Request) (res ctrl.Result, reterr error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "talosControlPlane", req.Name)
	ctx := context.Background()

	// Fetch the TalosControlPlane instance.
	tcp := &controlplanev1.TalosControlPlane{}
	if err := r.Client.Get(ctx, req.NamespacedName, tcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, tcp.ObjectMeta)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to retrieve owner Cluster from the API Server")

			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	if cluster == nil {
		logger.Info("Cluster Controller has not yet set OwnerRef")
		return ctrl.Result{Requeue: true}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	if util.IsPaused(cluster, tcp) {
		logger.Info("Reconciliation is paused for this object")
		return ctrl.Result{Requeue: true}, nil
	}

	// Wait for the cluster infrastructure to be ready before creating machines
	if !cluster.Status.InfrastructureReady {
		logger.Info("Cluster infra not ready")

		return ctrl.Result{Requeue: true}, nil
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(tcp, r.Client)
	if err != nil {
		logger.Error(err, "Failed to configure the patch helper")
		return ctrl.Result{Requeue: true}, nil
	}

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer) {
		controllerutil.AddFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)

		// patch and return right away instead of reusing the main defer,
		// because the main defer may take too much time to get cluster status
		// Patch ObservedGeneration only if the reconciliation completed successfully
		patchOpts := []patch.Option{}
		if reterr == nil {
			patchOpts = append(patchOpts, patch.WithStatusObservedGeneration{})
		}

		if err := patchHelper.Patch(ctx, tcp, patchOpts...); err != nil {
			logger.Error(err, "Failed to add finalizer to TalosControlPlane")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// nb: we moved the deletion reconcile before the defer to avoid additional, unneccessary patching.
	// we handle the patches necessary directly in the reconcileDelete function and will eventually get rid of this defer altogether.
	if !tcp.ObjectMeta.DeletionTimestamp.IsZero() {
		// Handle deletion reconciliation loop.
		return r.reconcileDelete(ctx, cluster, tcp)
	}

	defer func() {
		r.Log.Info("Attempting to set control plane status")

		if requeueErr, ok := errors.Cause(reterr).(capierrors.HasRequeueAfterError); ok {
			if res.RequeueAfter == 0 {
				res.RequeueAfter = requeueErr.GetRequeueAfter()
				reterr = nil
			}
		}

		// Always attempt to update status.
		if err := r.updateStatus(ctx, tcp, cluster); err != nil {
			logger.Error(err, "Failed to update TalosControlPlane Status")

			reterr = kerrors.NewAggregate([]error{reterr, err})
		}

		// Always attempt to Patch the TalosControlPlane object and status after each reconciliation.
		if err := r.Client.Status().Update(ctx, tcp); err != nil {
			logger.Error(err, "Failed to patch TalosControlPlane")
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}

		// TODO: remove this as soon as we have a proper remote cluster cache in place.
		// Make TCP to requeue in case status is not ready, so we can check for node status without waiting for a full resync (by default 10 minutes).
		// Only requeue if we are not going in exponential backoff due to error, or if we are not already re-queueing, or if the object has a deletion timestamp.
		if reterr == nil && !res.Requeue && !(res.RequeueAfter > 0) && tcp.ObjectMeta.DeletionTimestamp.IsZero() {
			if !tcp.Status.Ready || tcp.Status.UnavailableReplicas > 0 {
				res = ctrl.Result{RequeueAfter: 20 * time.Second}
			}
		}

		r.Log.Info("Successfully updated control plane status")
	}()

	// Update ownerrefs on infra templates
	if err := r.addClusterOwnerToObj(ctx, tcp.Spec.InfrastructureTemplate, cluster); err != nil {
		return ctrl.Result{}, err
	}

	// If ControlPlaneEndpoint is not set, return early
	if cluster.Spec.ControlPlaneEndpoint.IsZero() {
		logger.Info("Cluster does not yet have a ControlPlaneEndpoint defined")
		return ctrl.Result{}, nil
	}

	// TODO: handle proper adoption of Machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		logger.Error(err, "failed to retrieve control plane machines for cluster")
		return ctrl.Result{}, err
	}

	controlPlane := newControlPlane(cluster, tcp, ownedMachines)

	// If we've made it this far, we can assume that all ownedMachines are up to date
	numMachines := len(ownedMachines)
	desiredReplicas := int(*tcp.Spec.Replicas)

	requeue := false

	// Audit the etcd member list to remove any nodes that no longer exist
	if err := r.auditEtcd(ctx, util.ObjectKey(cluster), controlPlane.TCP.Name); err != nil {
		logger.Info("failed to check etcd membership list", "error", err)

		// if audit failed, requeue the reconcile in any case
		requeue = true
	}

	switch {
	// We are creating the first replica
	case numMachines < desiredReplicas && numMachines == 0:
		// Create new Machine w/ init
		logger.Info("Initializing control plane", "Desired", desiredReplicas, "Existing", numMachines)
		return r.bootControlPlane(ctx, cluster, tcp, controlPlane, initNode)
	// We are scaling up
	case numMachines < desiredReplicas && numMachines > 0:
		// Create a new Machine w/ join
		logger.Info("Scaling up control plane", "Desired", desiredReplicas, "Existing", numMachines)
		return r.bootControlPlane(ctx, cluster, tcp, controlPlane, controlplaneNode)
	// We are scaling down
	case numMachines > desiredReplicas:
		logger.Info("Scaling down control plane", "Desired", desiredReplicas, "Existing", numMachines)

		res, err = r.scaleDownControlPlane(ctx, util.ObjectKey(cluster), controlPlane.TCP.Name, ownedMachines)
		if err != nil {
			if res.Requeue || res.RequeueAfter > 0 {
				logger.Info("Failed to scale down control plane", "error", err)

				return res, nil
			}
		}

		return res, err
	}

	// Generate Cluster Kubeconfig if needed
	if err := r.reconcileKubeconfig(ctx, util.ObjectKey(cluster), cluster.Spec.ControlPlaneEndpoint, tcp); err != nil {
		logger.Error(err, "failed to reconcile Kubeconfig")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: requeue}, nil
}

// ClusterToTalosControlPlane is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for TalosControlPlane based on updates to a Cluster.
func (r *TalosControlPlaneReconciler) ClusterToTalosControlPlane(o handler.MapObject) []ctrl.Request {
	c, ok := o.Object.(*capiv1.Cluster)
	if !ok {
		r.Log.Error(nil, fmt.Sprintf("Expected a Cluster but got a %T", o.Object))
		return nil
	}

	controlPlaneRef := c.Spec.ControlPlaneRef
	if controlPlaneRef != nil && controlPlaneRef.Kind == "TalosControlPlane" {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: controlPlaneRef.Namespace, Name: controlPlaneRef.Name}}}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) reconcileDelete(ctx context.Context, cluster *capiv1.Cluster, tcp *controlplanev1.TalosControlPlane) (ctrl.Result, error) {
	// Get list of all control plane machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		r.Log.Error(err, "Failed to retrieve control plane machines for cluster")

		return ctrl.Result{}, err
	}

	// If no control plane machines remain, remove the finalizer
	if len(ownedMachines) == 0 {
		controllerutil.RemoveFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, tcp)
	}

	for _, ownedMachine := range ownedMachines {
		// Already deleting this machine
		if !ownedMachine.ObjectMeta.DeletionTimestamp.IsZero() {
			continue
		}
		// Submit deletion request
		if err := r.Client.Delete(ctx, &ownedMachine); err != nil && !apierrors.IsNotFound(err) {
			r.Log.Error(err, "Failed to cleanup owned machine")
			return ctrl.Result{}, err
		}
	}

	// Requeue the deletion so we can check to make sure machines got cleaned up
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// newControlPlane returns an instantiated ControlPlane.
func newControlPlane(cluster *capiv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines []capiv1.Machine) *ControlPlane {
	return &ControlPlane{
		TCP:      tcp,
		Cluster:  cluster,
		Machines: machines,
	}
}

func (r *TalosControlPlaneReconciler) scaleDownControlPlane(ctx context.Context, cluster client.ObjectKey, cpName string, machines []capiv1.Machine) (ctrl.Result, error) {
	if len(machines) == 0 {
		return ctrl.Result{}, fmt.Errorf("no machines found")
	}

	r.Log.Info("Found control plane machines", "machines", len(machines))

	clientset, err := r.kubeconfigForCluster(ctx, cluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	oldest := machines[0]
	for _, machine := range machines {
		if !machine.ObjectMeta.DeletionTimestamp.IsZero() {
			r.Log.Info("Machine is in process of deletion", "machine", machine.Name)

			node, err := clientset.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
			if err != nil {
				// It's possible for the node to already be deleted in the workload cluster, so we just
				// requeue if that's that case instead of throwing a scary error.
				if apierrors.IsNotFound(err) {
					return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
				}
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			r.Log.Info("Deleting node", "machine", machine.Name, "node", node.Name)

			err = clientset.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
			if err != nil {
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
		}

		if machine.CreationTimestamp.Before(&oldest.CreationTimestamp) {
			oldest = machine
		}
	}

	if oldest.Status.NodeRef == nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, fmt.Errorf("%q machine does not have a nodeRef", oldest.Name)
	}

	node := oldest.Status.NodeRef

	c, err := r.talosconfigForMachine(ctx, clientset, oldest)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	err = r.gracefulEtcdLeave(ctx, c, cluster, oldest)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info("Deleting machine", "machine", oldest.Name, "node", node.Name)

	err = r.Client.Delete(ctx, &oldest)
	if err != nil {
		return ctrl.Result{}, err
	}

	// NB: We shutdown the node here so that a loadbalancer will drop the backend.
	// The Kubernetes API server is configured to talk to etcd on localhost, but
	// at this point etcd has been stopped.
	r.Log.Info("Shutting down node", "machine", oldest.Name, "node", node.Name)

	err = c.Shutdown(ctx)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	r.Log.Info("Deleting node", "machine", oldest.Name, "node", node.Name)

	err = clientset.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	// Requeue so that we handle any additional scaling.
	return ctrl.Result{Requeue: true}, nil
}

func (r *TalosControlPlaneReconciler) getControlPlaneMachinesForCluster(ctx context.Context, cluster client.ObjectKey, cpName string) ([]capiv1.Machine, error) {
	selector := map[string]string{
		capiv1.ClusterLabelName:             cluster.Name,
		capiv1.MachineControlPlaneLabelName: "",
	}

	machineList := capiv1.MachineList{}
	if err := r.Client.List(
		ctx,
		&machineList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(selector),
	); err != nil {
		return nil, err
	}

	return machineList.Items, nil

}

// getFailureDomain will return a slice of failure domains from the cluster status.
func (r *TalosControlPlaneReconciler) getFailureDomain(ctx context.Context, cluster *capiv1.Cluster) []string {
	if cluster.Status.FailureDomains == nil {
		return nil
	}

	retList := []string{}
	for key := range cluster.Status.FailureDomains {
		retList = append(retList, key)
	}
	return retList
}

func (r *TalosControlPlaneReconciler) bootControlPlane(ctx context.Context, cluster *capiv1.Cluster, tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane, nodeType NodeType) (ctrl.Result, error) {
	// Since the cloned resource should eventually have a controller ref for the Machine, we create an
	// OwnerReference here without the Controller field set
	infraCloneOwner := &metav1.OwnerReference{
		APIVersion: controlplanev1.GroupVersion.String(),
		Kind:       "TalosControlPlane",
		Name:       tcp.Name,
		UID:        tcp.UID,
	}

	// Clone the infrastructure template
	infraRef, err := external.CloneTemplate(ctx, &external.CloneTemplateInput{
		Client:      r.Client,
		TemplateRef: &tcp.Spec.InfrastructureTemplate,
		Namespace:   tcp.Namespace,
		OwnerRef:    infraCloneOwner,
		ClusterName: cluster.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapConfig := &tcp.Spec.ControlPlaneConfig.InitConfig
	if nodeType == controlplaneNode {
		bootstrapConfig = &tcp.Spec.ControlPlaneConfig.ControlPlaneConfig
	}

	// Clone the bootstrap configuration
	bootstrapRef, err := r.generateTalosConfig(ctx, tcp, cluster, bootstrapConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	machine := &capiv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.SimpleNameGenerator.GenerateName(tcp.Name + "-"),
			Namespace: tcp.Namespace,
			Labels: map[string]string{
				capiv1.ClusterLabelName:             cluster.ClusterName,
				capiv1.MachineControlPlaneLabelName: "",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: capiv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           &tcp.Spec.Version,
			InfrastructureRef: *infraRef,
			Bootstrap: capiv1.Bootstrap{
				ConfigRef: bootstrapRef,
			},
		},
	}

	failureDomains := r.getFailureDomain(ctx, cluster)
	if len(failureDomains) > 0 {
		machine.Spec.FailureDomain = &failureDomains[rand.Intn(len(failureDomains))]
	}

	if err := r.Client.Create(ctx, machine); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "Failed to create machine")
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *TalosControlPlaneReconciler) generateTalosConfig(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *capiv1.Cluster, spec *cabptv1.TalosConfigSpec) (*corev1.ObjectReference, error) {
	owner := metav1.OwnerReference{
		APIVersion:         controlplanev1.GroupVersion.String(),
		Kind:               "TalosControlPlane",
		Name:               tcp.Name,
		UID:                tcp.UID,
		BlockOwnerDeletion: pointer.BoolPtr(true),
	}

	bootstrapConfig := &cabptv1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:            names.SimpleNameGenerator.GenerateName(tcp.Name + "-"),
			Namespace:       tcp.Namespace,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: *spec,
	}

	if err := r.Client.Create(ctx, bootstrapConfig); err != nil {
		return nil, errors.Wrap(err, "Failed to create bootstrap configuration")
	}

	bootstrapRef := &corev1.ObjectReference{
		APIVersion: cabptv1.GroupVersion.String(),
		Kind:       "TalosConfig",
		Name:       bootstrapConfig.GetName(),
		Namespace:  bootstrapConfig.GetNamespace(),
		UID:        bootstrapConfig.GetUID(),
	}

	return bootstrapRef, nil
}

func (r *TalosControlPlaneReconciler) updateStatus(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *capiv1.Cluster) error {
	clusterSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			capiv1.ClusterLabelName:             cluster.Name,
			capiv1.MachineControlPlaneLabelName: "",
		},
	}

	selector, err := metav1.LabelSelectorAsSelector(clusterSelector)
	if err != nil {
		// Since we are building up the LabelSelector above, this should not fail
		return errors.Wrap(err, "failed to parse label selector")
	}
	// Copy label selector to its status counterpart in string format.
	// This is necessary for CRDs including scale subresources.
	tcp.Status.Selector = selector.String()

	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		return err
	}

	replicas := int32(len(ownedMachines))

	// set basic data that does not require interacting with the workload cluster
	tcp.Status.Ready = false
	tcp.Status.Replicas = replicas
	tcp.Status.ReadyReplicas = 0
	tcp.Status.UnavailableReplicas = replicas

	// Return early if the deletion timestamp is set, we don't want to try to connect to the workload cluster.
	if !tcp.DeletionTimestamp.IsZero() {
		return nil
	}

	clientset, err := r.kubeconfigForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		return err
	}

	errChan := make(chan error)

	for _, ownedMachine := range ownedMachines {
		ownedMachine := ownedMachine

		go func() {
			e := func() error {
				if capiv1.MachinePhase(ownedMachine.Status.Phase) == capiv1.MachinePhaseDeleting {
					return fmt.Errorf("machine is deleting")
				}

				if ownedMachine.Status.NodeRef == nil {
					return fmt.Errorf("machine %q does not have a noderef", ownedMachine.Name)
				}

				node, err := clientset.CoreV1().Nodes().Get(ctx, ownedMachine.Status.NodeRef.Name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get node %q: %w", node.Name, err)
				}

				for _, condition := range node.Status.Conditions {
					if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
						return nil
					}
				}

				return fmt.Errorf("node ready condition not found")
			}()

			if e != nil {
				e = fmt.Errorf("failed to get status for %q: %w", ownedMachine.Name, e)
			}

			errChan <- e
		}()
	}

	for range ownedMachines {
		err = <-errChan
		if err == nil {
			tcp.Status.ReadyReplicas++
		} else {
			r.Log.Info("Failed to get readiness of machine", "err", err)
		}
	}

	tcp.Status.UnavailableReplicas = replicas - tcp.Status.ReadyReplicas

	if tcp.Status.ReadyReplicas > 0 {
		r.Log.Info("Ready replicas", "count", tcp.Status.ReadyReplicas)
		tcp.Status.Ready = true
	}

	// We consider ourselves "initialized" if the workload cluster returns any number of nodes.
	// We also do not return client list errors (just log them) as it's expected that it will fail
	// for a while until the cluster is up.
	nodeList, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		if len(nodeList.Items) > 0 {
			tcp.Status.Initialized = true
		}
	} else {
		r.Log.Error(err, "Failed attempt to contact workload cluster")
	}

	return nil
}

func (r *TalosControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, clusterName client.ObjectKey, endpoint capiv1.APIEndpoint, kcp *controlplanev1.TalosControlPlane) error {
	if endpoint.IsZero() {
		return nil
	}

	_, err := secret.GetFromNamespacedName(ctx, r.Client, clusterName, secret.Kubeconfig)
	switch {
	case apierrors.IsNotFound(err):
		createErr := kubeconfig.CreateSecretWithOwner(
			ctx,
			r.Client,
			clusterName,
			endpoint.String(),
			*metav1.NewControllerRef(kcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
		)
		if createErr != nil {
			if createErr == kubeconfig.ErrDependentCertificateNotFound {
				return errors.Wrapf(&capierrors.RequeueAfterError{RequeueAfter: 30 * time.Second},
					"could not find secret %q for Cluster %q in namespace %q, requeuing",
					secret.ClusterCA, clusterName.Name, clusterName.Namespace)
			}
			return createErr
		}
	case err != nil:
		return errors.Wrapf(err, "failed to retrieve kubeconfig Secret for Cluster %q in namespace %q", clusterName.Name, clusterName.Namespace)
	}

	return nil
}

func (r *TalosControlPlaneReconciler) addClusterOwnerToObj(ctx context.Context, ref corev1.ObjectReference, cluster *capiv1.Cluster) error {
	obj, err := external.Get(ctx, r.Client, &ref, cluster.Namespace)
	if err != nil {
		return err
	}

	objPatchHelper, err := patch.NewHelper(obj, r.Client)
	if err != nil {
		return err
	}

	obj.SetOwnerReferences(util.EnsureOwnerRef(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: capiv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
	}))

	return objPatchHelper.Patch(ctx, obj)
}
