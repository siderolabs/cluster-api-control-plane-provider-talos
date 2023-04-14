// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const requeueDuration = 30 * time.Second

// TalosControlPlaneReconciler reconciles a TalosControlPlane object
type TalosControlPlaneReconciler struct {
	client.Client
	APIReader client.Reader
	Log       logr.Logger
	Scheme    *runtime.Scheme
	Tracker   *remote.ClusterCacheTracker
}

func (r *TalosControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1.TalosControlPlane{}).
		Owns(&clusterv1.Machine{}).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(r.ClusterToTalosControlPlane),
		).
		WithOptions(options).
		Complete(r)
}

// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;patch;update
// +kubebuilder:rbac:groups=core,resources=configmaps,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac,resources=roles,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac,resources=rolebindings,namespace=kube-system,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io;bootstrap.cluster.x-k8s.io;controlplane.cluster.x-k8s.io,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch;create;update;patch;delete

func (r *TalosControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, reterr error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "talosControlPlane", req.Name)

	// Fetch the TalosControlPlane instance.
	tcp := &controlplanev1.TalosControlPlane{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, tcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, tcp.ObjectMeta)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to retrieve owner Cluster from the API Server")

			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}

	if cluster == nil {
		logger.Info("cluster Controller has not yet set OwnerRef")
		return ctrl.Result{Requeue: true}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	if annotations.IsPaused(cluster, tcp) {
		logger.Info("reconciliation is paused for this object")
		return ctrl.Result{Requeue: true}, nil
	}

	// Wait for the cluster infrastructure to be ready before creating machines
	if !cluster.Status.InfrastructureReady {
		logger.Info("cluster infra not ready")

		return ctrl.Result{Requeue: true}, nil
	}

	// Initialize the patch helper.
	patchHelper, err := patch.NewHelper(tcp, r.Client)
	if err != nil {
		logger.Error(err, "failed to configure the patch helper")
		return ctrl.Result{Requeue: true}, nil
	}

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer) {
		controllerutil.AddFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)

		// patch and return right away instead of reusing the main defer,
		// because the main defer may take too much time to get cluster status

		if err := patchTalosControlPlane(ctx, patchHelper, tcp, patch.WithStatusObservedGeneration{}); err != nil {
			logger.Error(err, "failed to add finalizer to TalosControlPlane")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	defer func() {
		r.Log.Info("attempting to set control plane status")

		// Always attempt to update status.
		if err := r.updateStatus(ctx, tcp, cluster); err != nil {
			logger.Error(err, "failed to update TalosControlPlane Status")

			reterr = kerrors.NewAggregate([]error{reterr, err})
		}

		// Always attempt to Patch the TalosControlPlane object and status after each reconciliation.
		if err := patchTalosControlPlane(ctx, patchHelper, tcp, patch.WithStatusObservedGeneration{}); err != nil {
			logger.Error(err, "failed to patch TalosControlPlane")
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}

		// TODO: remove this as soon as we have a proper remote cluster cache in place.
		// Make TCP to requeue in case status is not ready, so we can check for node status without waiting for a full resync (by default 10 minutes).
		// Only requeue if we are not going in exponential backoff due to error, or if we are not already re-queueing, or if the object has a deletion timestamp.
		if reterr == nil && !res.Requeue && res.RequeueAfter <= 0 && tcp.ObjectMeta.DeletionTimestamp.IsZero() {
			if !tcp.Status.Ready || tcp.Status.UnavailableReplicas > 0 {
				res = ctrl.Result{RequeueAfter: 20 * time.Second}
			}
		}

		r.Log.Info("successfully updated control plane status")
	}()

	if !tcp.ObjectMeta.DeletionTimestamp.IsZero() {
		// Handle deletion reconciliation loop.
		return r.reconcileDelete(ctx, cluster, tcp)
	}

	return r.reconcile(ctx, cluster, tcp)
}

func (r *TalosControlPlaneReconciler) reconcile(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane) (res ctrl.Result, err error) {
	logger := ctrl.LoggerFrom(ctx, "cluster", cluster.Name)
	logger.Info("reconcile TalosControlPlane")

	// Update ownerrefs on infra templates
	if err := r.reconcileExternalReference(ctx, tcp.Spec.InfrastructureTemplate, cluster); err != nil {
		return ctrl.Result{}, err
	}

	// If ControlPlaneEndpoint is not set, return early
	if !cluster.Spec.ControlPlaneEndpoint.IsValid() {
		logger.Info("cluster does not yet have a ControlPlaneEndpoint defined")
		return ctrl.Result{}, nil
	}

	// TODO: handle proper adoption of Machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		logger.Error(err, "failed to retrieve control plane machines for cluster")
		return ctrl.Result{}, err
	}

	conditionGetters := make([]conditions.Getter, len(ownedMachines.Items))

	for i, v := range ownedMachines.Items {
		conditionGetters[i] = &v
	}

	conditions.SetAggregate(tcp, controlplanev1.MachinesReadyCondition, conditionGetters, conditions.AddSourceRef(), conditions.WithStepCounterIf(false))

	var (
		errs        error
		result      ctrl.Result
		phaseResult ctrl.Result
	)

	// run all similar reconcile steps in the loop and pick the lowest RetryAfter, aggregate errors and check the requeue flags.
	for _, phase := range []func(context.Context, *clusterv1.Cluster, *controlplanev1.TalosControlPlane, *clusterv1.MachineList) (ctrl.Result, error){
		r.reconcileEtcdMembers,
		r.reconcileNodeHealth,
		r.reconcileConditions,
		r.reconcileKubeconfig,
		r.reconcileMachines,
	} {
		phaseResult, err = phase(ctx, cluster, tcp, &ownedMachines)
		if err != nil {
			errs = kerrors.NewAggregate([]error{errs, err})
		}

		result = util.LowestNonZeroResult(result, phaseResult)
	}

	return result, errs
}

// ClusterToTalosControlPlane is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for TalosControlPlane based on updates to a Cluster.
func (r *TalosControlPlaneReconciler) ClusterToTalosControlPlane(o client.Object) []ctrl.Request {
	c, ok := o.(*clusterv1.Cluster)
	if !ok {
		r.Log.Error(nil, fmt.Sprintf("expected a Cluster but got a %T", o))
		return nil
	}

	controlPlaneRef := c.Spec.ControlPlaneRef
	if controlPlaneRef != nil && controlPlaneRef.Kind == "TalosControlPlane" {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: controlPlaneRef.Namespace, Name: controlPlaneRef.Name}}}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) reconcileDelete(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane) (ctrl.Result, error) {
	// Get list of all control plane machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		r.Log.Error(err, "failed to retrieve control plane machines for cluster")

		return ctrl.Result{}, err
	}

	// If no control plane machines remain, remove the finalizer
	if len(ownedMachines.Items) == 0 {
		controllerutil.RemoveFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, tcp)
	}

	for _, ownedMachine := range ownedMachines.Items {
		// Already deleting this machine
		if !ownedMachine.ObjectMeta.DeletionTimestamp.IsZero() {
			continue
		}
		// Submit deletion request
		if err := r.Client.Delete(ctx, &ownedMachine); err != nil && !apierrors.IsNotFound(err) {
			r.Log.Error(err, "failed to cleanup owned machine")
			return ctrl.Result{}, err
		}
	}

	conditions.MarkFalse(tcp, controlplanev1.ResizedCondition, clusterv1.DeletingReason, clusterv1.ConditionSeverityInfo, "")
	// Requeue the deletion so we can check to make sure machines got cleaned up
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

func (r *TalosControlPlaneReconciler) getControlPlaneMachinesForCluster(ctx context.Context, cluster client.ObjectKey, cpName string) (clusterv1.MachineList, error) {
	selector := map[string]string{
		clusterv1.ClusterLabelName:             cluster.Name,
		clusterv1.MachineControlPlaneLabelName: "",
	}

	machineList := clusterv1.MachineList{}
	if err := r.Client.List(
		ctx,
		&machineList,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(selector),
	); err != nil {
		return machineList, err
	}

	return machineList, nil
}

// getFailureDomain will return a slice of failure domains from the cluster status.
func (r *TalosControlPlaneReconciler) getFailureDomain(ctx context.Context, cluster *clusterv1.Cluster) []string {
	if cluster.Status.FailureDomains == nil {
		return nil
	}

	retList := []string{}
	for key := range cluster.Status.FailureDomains {
		retList = append(retList, key)
	}
	return retList
}

func (r *TalosControlPlaneReconciler) bootControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane, first bool) (ctrl.Result, error) {
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
		conditions.MarkFalse(tcp, controlplanev1.MachinesCreatedCondition, controlplanev1.InfrastructureTemplateCloningFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, err
	}

	bootstrapConfig := &tcp.Spec.ControlPlaneConfig.ControlPlaneConfig
	if !reflect.ValueOf(tcp.Spec.ControlPlaneConfig.InitConfig).IsZero() && first {
		bootstrapConfig = &tcp.Spec.ControlPlaneConfig.InitConfig
	}

	// Clone the bootstrap configuration
	bootstrapRef, err := r.generateTalosConfig(ctx, tcp, cluster, bootstrapConfig)
	if err != nil {
		conditions.MarkFalse(tcp, controlplanev1.MachinesCreatedCondition, controlplanev1.BootstrapTemplateCloningFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, err
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.SimpleNameGenerator.GenerateName(tcp.Name + "-"),
			Namespace: tcp.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterLabelName:             cluster.Name,
				clusterv1.MachineControlPlaneLabelName: "",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           &tcp.Spec.Version,
			InfrastructureRef: *infraRef,
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: bootstrapRef,
			},
		},
	}

	failureDomains := r.getFailureDomain(ctx, cluster)
	if len(failureDomains) > 0 {
		machine.Spec.FailureDomain = &failureDomains[rand.Intn(len(failureDomains))]
	}

	if err := r.Client.Create(ctx, machine); err != nil {
		conditions.MarkFalse(tcp, controlplanev1.MachinesCreatedCondition, controlplanev1.MachineGenerationFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, errors.Wrap(err, "Failed to create machine")
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *TalosControlPlaneReconciler) bootstrapCluster(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster, machines []clusterv1.Machine) error {
	c, err := r.talosconfigForMachines(ctx, tcp, machines...)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	addresses := []string{}
	for _, machine := range machines {
		found := false

		// Prefer finding an InternalIP address for the machine first.
		for _, addr := range machine.Status.Addresses {
			if addr.Type == clusterv1.MachineInternalIP {
				addresses = append(addresses, addr.Address)

				found = true

				break
			}
		}

		if found {
			continue
		}

		// Fallback to finding an ExternalIP address for the machine
		// if no InternalIP is found.
		for _, addr := range machine.Status.Addresses {
			if addr.Type == clusterv1.MachineExternalIP {
				addresses = append(addresses, addr.Address)

				found = true

				break
			}
		}

		if !found {
			return fmt.Errorf("machine %q doesn't have an any InternalIP or ExternalIP address yet", machine.Name)
		}
	}

	if len(addresses) == 0 {
		return fmt.Errorf("no machine addresses to use for bootstrap")
	}

	list, err := c.LS(talosclient.WithNodes(ctx, addresses...), &machineapi.ListRequest{Root: "/var/lib/etcd/member"})
	if err != nil {
		return err
	}

	for {
		info, err := list.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || talosclient.StatusCode(err) == codes.Canceled {
				break
			}

			return err
		}

		// if the directory exists at least on a single node it means that cluster
		// was already bootstrapped
		if info.Metadata.Error == "" {
			return nil
		}
	}

	sort.Strings(addresses)

	if err := c.Bootstrap(talosclient.WithNodes(ctx, addresses[0]), &machineapi.BootstrapRequest{}); err != nil {
		if status.Code(err) != codes.AlreadyExists {
			return err
		}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) generateTalosConfig(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster, spec *cabptv1.TalosConfigSpec) (*corev1.ObjectReference, error) {
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

func (r *TalosControlPlaneReconciler) updateStatus(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster) error {
	clusterSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			clusterv1.ClusterLabelName:             cluster.Name,
			clusterv1.MachineControlPlaneLabelName: "",
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

	replicas := int32(len(ownedMachines.Items))

	// set basic data that does not require interacting with the workload cluster
	tcp.Status.Ready = false
	tcp.Status.Replicas = replicas
	tcp.Status.ReadyReplicas = 0
	tcp.Status.UnavailableReplicas = replicas

	// Return early if the deletion timestamp is set, we don't want to try to connect to the workload cluster.
	if !tcp.DeletionTimestamp.IsZero() {
		return nil
	}

	c, err := r.Tracker.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		r.Log.Info("failed to get kubeconfig for the cluster", "error", err)

		return nil
	}

	nodeSelector := labels.NewSelector()
	req, err := labels.NewRequirement(constants.LabelNodeRoleControlPlane, selection.Exists, []string{})
	if err != nil {
		return err
	}

	var nodes v1.NodeList

	err = c.List(ctx, &nodes, &client.ListOptions{
		LabelSelector: nodeSelector.Add(*req),
	})

	if err != nil {
		r.Log.Info("failed to list controlplane nodes", "error", err)

		return nil
	}

	// if we were able to fetch some resources via control plane endpoint,
	// workload cluster control plane endpoint is available
	tcp.Status.Initialized = true
	conditions.MarkTrue(tcp, controlplanev1.AvailableCondition)

	for _, node := range nodes.Items {
		if util.IsNodeReady(&node) {
			tcp.Status.ReadyReplicas++
		}
	}

	// fix the case then some Node objects are still visible which were deleted
	if tcp.Status.ReadyReplicas > tcp.Status.Replicas {
		tcp.Status.ReadyReplicas = tcp.Status.Replicas
	}

	tcp.Status.UnavailableReplicas = replicas - tcp.Status.ReadyReplicas

	if tcp.Status.ReadyReplicas > 0 {
		tcp.Status.Ready = true
	}

	r.Log.Info("ready replicas", "count", tcp.Status.ReadyReplicas)

	return nil
}

func (r *TalosControlPlaneReconciler) reconcileExternalReference(ctx context.Context, ref corev1.ObjectReference, cluster *clusterv1.Cluster) error {
	obj, err := external.Get(ctx, r.Client, &ref, cluster.Namespace)
	if err != nil {
		return err
	}

	objPatchHelper, err := patch.NewHelper(obj, r.Client)
	if err != nil {
		return err
	}

	obj.SetOwnerReferences(util.EnsureOwnerRef(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
	}))

	return objPatchHelper.Patch(ctx, obj)
}

func (r *TalosControlPlaneReconciler) reconcileKubeconfig(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (ctrl.Result, error) {
	endpoint := cluster.Spec.ControlPlaneEndpoint
	if endpoint.IsZero() {
		return ctrl.Result{}, nil
	}

	clusterName := util.ObjectKey(cluster)
	existingKubeconfig, err := secret.GetFromNamespacedName(ctx, r.Client, clusterName, secret.Kubeconfig)
	switch {
	case apierrors.IsNotFound(err):
		createErr := kubeconfig.CreateSecretWithOwner(
			ctx,
			r.Client,
			clusterName,
			endpoint.String(),
			*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
		)
		if createErr != nil {
			if errors.Is(createErr, kubeconfig.ErrDependentCertificateNotFound) {
				r.Log.Info("could not find secret", "secret", secret.ClusterCA, "cluster", clusterName.Name, "namespace", clusterName.Namespace)

				return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
			}

			return ctrl.Result{}, createErr
		}
	case err != nil:
		return ctrl.Result{RequeueAfter: 20 * time.Second}, fmt.Errorf("failed to retrieve kubeconfig Secret for Cluster %q in namespace %q: %w", clusterName.Name, clusterName.Namespace, err)
	default:
		// kubeconfig is already generated
		needsRotation, err := kubeconfig.NeedsClientCertRotation(existingKubeconfig, certs.ClientCertificateRenewalDuration)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to figure out if we need to regenerate cluster client cert: %w", err)
		}

		if !needsRotation {
			return ctrl.Result{}, nil
		}

		r.Log.Info("kubeconfig certificate rotation", "secret", secret.Kubeconfig, "cluster", clusterName.Name, "namespace", clusterName.Namespace)

		err = kubeconfig.RegenerateSecret(ctx, r.Client, existingKubeconfig)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to regenerate kubeconfig: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileEtcdMembers(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	var errs error
	// Audit the etcd member list to remove any nodes that no longer exist
	if err := r.auditEtcd(ctx, tcp, util.ObjectKey(cluster), tcp.Name); err != nil {
		errs = kerrors.NewAggregate([]error{errs, err})
	}

	if err := r.etcdHealthcheck(ctx, tcp, cluster, machines.Items); err != nil {
		conditions.MarkFalse(tcp, controlplanev1.EtcdClusterHealthyCondition, controlplanev1.EtcdClusterUnhealthyReason,
			clusterv1.ConditionSeverityWarning, err.Error())
		errs = kerrors.NewAggregate([]error{errs, err})
	} else {
		conditions.MarkTrue(tcp, controlplanev1.EtcdClusterHealthyCondition)
	}

	if errs != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errs
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileNodeHealth(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if err := r.nodesHealthcheck(ctx, tcp, cluster, machines.Items); err != nil {
		reason := controlplanev1.ControlPlaneComponentsInspectionFailedReason

		if errors.Is(err, &errServiceUnhealthy{}) {
			reason = controlplanev1.ControlPlaneComponentsUnhealthyReason
		}

		conditions.MarkFalse(tcp, controlplanev1.ControlPlaneComponentsHealthyCondition, reason,
			clusterv1.ConditionSeverityWarning, err.Error())

		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	} else {
		conditions.MarkTrue(tcp, controlplanev1.ControlPlaneComponentsHealthyCondition)
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileConditions(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if !conditions.Has(tcp, controlplanev1.AvailableCondition) {
		conditions.MarkFalse(tcp, controlplanev1.AvailableCondition, controlplanev1.WaitingForTalosBootReason, clusterv1.ConditionSeverityInfo, "")
	}

	if !conditions.Has(tcp, controlplanev1.MachinesBootstrapped) {
		conditions.MarkFalse(tcp, controlplanev1.MachinesBootstrapped, controlplanev1.WaitingForMachinesReason, clusterv1.ConditionSeverityInfo, "")
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileMachines(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (res ctrl.Result, err error) {
	logger := r.Log.WithValues("namespace", tcp.Namespace, "talosControlPlane", tcp.Name)

	// If we've made it this far, we can assume that all ownedMachines are up to date
	numMachines := len(machines.Items)
	desiredReplicas := 0

	if tcp.Spec.Replicas != nil {
		desiredReplicas = int(*tcp.Spec.Replicas)
	}

	controlPlane, err := newControlPlane(ctx, r.Client, cluster, tcp, collections.FromMachineList(machines))
	if err != nil {
		return ctrl.Result{}, err
	}

	needRollout := controlPlane.MachinesNeedingRollout()
	if len(needRollout) > 0 {
		logger.Info("rolling out control plane machines", "needRollout", needRollout.Names())
		conditions.MarkFalse(controlPlane.TCP,
			controlplanev1.MachinesSpecUpToDateCondition,
			controlplanev1.RollingUpdateInProgressReason,
			clusterv1.ConditionSeverityWarning, "Rolling %d replicas with outdated spec (%d replicas up to date)", len(needRollout), len(controlPlane.Machines)-len(needRollout))

		return r.upgradeControlPlane(ctx, cluster, tcp, controlPlane, needRollout)
	} else {
		if conditions.Has(controlPlane.TCP, controlplanev1.MachinesSpecUpToDateCondition) {
			conditions.MarkTrue(controlPlane.TCP, controlplanev1.MachinesSpecUpToDateCondition)
		}
	}

	switch {
	// We are creating the first replica
	case numMachines < desiredReplicas && numMachines == 0:
		// Create new Machine w/ init
		logger.Info("initializing control plane", "Desired", desiredReplicas, "Existing", numMachines)

		return r.bootControlPlane(ctx, cluster, tcp, controlPlane, true)
	// We are scaling up
	case numMachines < desiredReplicas && numMachines > 0:
		return r.scaleUpControlPlane(ctx, cluster, tcp, controlPlane)
	// We are scaling down
	case numMachines > desiredReplicas:
		res, err = r.scaleDownControlPlane(ctx, cluster, tcp, controlPlane, collections.Machines{})
		if err != nil {
			if res.Requeue || res.RequeueAfter > 0 {
				logger.Info("failed to scale down control plane", "error", err)

				return res, nil
			}
		}

		return res, err
	default:
		if !reflect.ValueOf(tcp.Spec.ControlPlaneConfig.InitConfig).IsZero() {
			tcp.Status.Bootstrapped = true
			conditions.MarkTrue(tcp, controlplanev1.MachinesBootstrapped)
		}

		if !tcp.Status.Bootstrapped {
			if err := r.bootstrapCluster(ctx, tcp, cluster, machines.Items); err != nil {
				conditions.MarkFalse(tcp, controlplanev1.MachinesBootstrapped, controlplanev1.WaitingForTalosBootReason, clusterv1.ConditionSeverityInfo, err.Error())

				logger.Info("bootstrap failed, retrying in 20 seconds", "error", err)

				return ctrl.Result{RequeueAfter: time.Second * 20}, nil
			}

			conditions.MarkTrue(tcp, controlplanev1.MachinesBootstrapped)

			tcp.Status.Bootstrapped = true
		}

		if conditions.Has(tcp, controlplanev1.MachinesReadyCondition) {
			conditions.MarkTrue(tcp, controlplanev1.ResizedCondition)
		}

		conditions.MarkTrue(tcp, controlplanev1.MachinesCreatedCondition)
	}

	return ctrl.Result{}, nil
}

func patchTalosControlPlane(ctx context.Context, patchHelper *patch.Helper, tcp *controlplanev1.TalosControlPlane, opts ...patch.Option) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	conditions.SetSummary(tcp,
		conditions.WithConditions(
			controlplanev1.MachinesCreatedCondition,
			controlplanev1.ResizedCondition,
			controlplanev1.MachinesReadyCondition,
			controlplanev1.AvailableCondition,
			controlplanev1.MachinesBootstrapped,
		),
	)

	opts = append(opts,
		patch.WithOwnedConditions{Conditions: []clusterv1.ConditionType{
			controlplanev1.MachinesCreatedCondition,
			clusterv1.ReadyCondition,
			controlplanev1.ResizedCondition,
			controlplanev1.MachinesReadyCondition,
			controlplanev1.AvailableCondition,
			controlplanev1.MachinesBootstrapped,
		}},
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		ctx,
		tcp,
		opts...,
	)
}
