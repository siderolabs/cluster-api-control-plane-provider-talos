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
	"strings"
	"time"

	"encoding/json"
	"hash/fnv"

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/utils/pointer"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/external"
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

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const requeueDuration = 30 * time.Second

// TalosControlPlaneReconciler reconciles a TalosControlPlane object
type TalosControlPlaneReconciler struct {
	client.Client
	APIReader    client.Reader
	Log          logr.Logger
	Scheme       *runtime.Scheme
	ClusterCache clustercache.ClusterCache
}

func (r *TalosControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1.TalosControlPlane{}).
		Owns(&clusterv1.Machine{}).
		Watches(
			&clusterv1.Cluster{},
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
	if !conditions.IsTrue(cluster, string(clusterv1.InfrastructureReadyCondition)) {
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

		logger.Info("successfully updated control plane status")
	}()

	if !tcp.ObjectMeta.DeletionTimestamp.IsZero() {
		// Handle deletion reconciliation loop.
		return r.reconcileDelete(ctx, cluster, tcp)
	}

	return r.reconcile(ctx, cluster, tcp)
}

// reconcileTalosControlPlaneStatus updates the v1beta2 status fields
func (r *TalosControlPlaneReconciler) reconcileTalosControlPlaneStatus(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	machines []*clusterv1.Machine,
) error {
	// Count machines in different states
	readyCount := countReadyMachines(machines)
	availableCount := countAvailableMachines(machines, tcp.Spec.MinReadySeconds)
	upToDateCount := countUpToDateMachines(machines, tcp)

	// Initialize v1beta2 status if not present
	if tcp.Status.V1Beta2 == nil {
		tcp.Status.V1Beta2 = &controlplanev1.TalosControlPlaneV1Beta2Status{}
	}

	// Update replica counts
	tcp.Status.V1Beta2.ReadyReplicas = ptr.To(int32(readyCount))
	tcp.Status.V1Beta2.AvailableReplicas = ptr.To(int32(availableCount))
	tcp.Status.V1Beta2.UpToDateReplicas = ptr.To(int32(upToDateCount))

	// Populate root-level replica fields read by CAPI core
	tcp.Status.ReadyReplicas = int32(readyCount)
	tcp.Status.AvailableReplicas = ptr.To(int32(availableCount))
	tcp.Status.UpToDateReplicas = ptr.To(int32(upToDateCount))

	// Update v1beta2 conditions
	r.updateV1Beta2Conditions(tcp, readyCount, availableCount, upToDateCount)
	if err := r.reconcileMachineUpToDateConditions(ctx, machines, tcp); err != nil {
		r.Log.Error(err, "failed to reconcile machine UpToDate conditions")
	}
	return nil
}

// reconcileMachineUpToDateConditions patches the UpToDate condition on each Machine
// based on whether its SpecHashAnnotation matches the current TalosControlPlane spec hash.
func (r *TalosControlPlaneReconciler) reconcileMachineUpToDateConditions(
	ctx context.Context,
	machines []*clusterv1.Machine,
	tcp *controlplanev1.TalosControlPlane,
) error {
	specHash, err := computeSpecHash(tcp)
	if err != nil {
		return errors.Wrap(err, "failed to compute spec hash")
	}

	for _, machine := range machines {
		upToDate := false
		if h, ok := machine.Annotations[controlplanev1.SpecHashAnnotation]; ok {
			upToDate = h == specHash
		}

		condStatus := metav1.ConditionFalse
		reason := "OutOfDate"
		message := "Machine spec does not match current TalosControlPlane spec"
		if upToDate {
			condStatus = metav1.ConditionTrue
			reason = "UpToDate"
			message = "Machine spec is up to date"
		}

		patch := client.MergeFrom(machine.DeepCopy())
		apimeta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
			Type:               clusterv1.MachineUpToDateCondition, // = "UpToDate"
			Status:             condStatus,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: machine.Generation,
		})
		if err := r.Client.Status().Patch(ctx, machine, patch); err != nil {
			return errors.Wrapf(err, "failed to patch UpToDate condition on machine %s", machine.Name)
		}
	}
	return nil
}

// countReadyMachines returns the number of machines with a Ready condition
func countReadyMachines(machines []*clusterv1.Machine) int {
	count := 0
	for _, machine := range machines {
		if conditions.IsTrue(machine, clusterv1.ReadyCondition) {
			count++
		}
	}
	return count
}

// countAvailableMachines returns the number of ready machines that have passed
// the MinReadySeconds threshold
func countAvailableMachines(machines []*clusterv1.Machine, minReadySeconds *int32) int {
	count := 0
	threshold := time.Duration(0)
	if minReadySeconds != nil {
		threshold = time.Duration(*minReadySeconds) * time.Second
	}

	for _, machine := range machines {
		if !conditions.IsTrue(machine, clusterv1.ReadyCondition) {
			continue
		}

		// Check if machine has been ready for at least MinReadySeconds
		readyCondition := conditions.Get(machine, clusterv1.ReadyCondition)
		if readyCondition != nil && time.Since(readyCondition.LastTransitionTime.Time) >= threshold {
			count++
		}
	}
	return count
}

// countUpToDateMachines returns the number of machines whose configuration
// matches the current TalosControlPlane spec
func countUpToDateMachines(machines []*clusterv1.Machine, tcp *controlplanev1.TalosControlPlane) int {
	count := 0

	// Compute the spec hash for the current TalosControlPlane
	specHash, err := computeSpecHash(tcp)
	if err != nil {
		// If hash computation fails, conservatively count no machines as up-to-date
		return 0
	}

	for _, machine := range machines {
		// Retrieve the spec hash annotation from the machine
		machineSpecHash, ok := machine.Annotations[controlplanev1.SpecHashAnnotation]
		if !ok {
			continue
		}

		// Compare hashes: if they match, machine is up-to-date
		if machineSpecHash == specHash {
			count++
		}
	}

	return count
}

// ensureV1Beta2 initializes the V1Beta2 status sub-object if nil.
func ensureV1Beta2(tcp *controlplanev1.TalosControlPlane) {
	if tcp.Status.V1Beta2 == nil {
		tcp.Status.V1Beta2 = &controlplanev1.TalosControlPlaneV1Beta2Status{}
	}
	if tcp.Status.V1Beta2.Conditions == nil {
		tcp.Status.V1Beta2.Conditions = []metav1.Condition{}
	}
}

// updateV1Beta2Conditions updates the v1beta2 contract conditions
func (r *TalosControlPlaneReconciler) updateV1Beta2Conditions(
	tcp *controlplanev1.TalosControlPlane,
	readyCount, availableCount, upToDateCount int,
) {
	desiredReplicas := int(*tcp.Spec.Replicas)

	// Ensure conditions slice exists
	if tcp.Status.V1Beta2.Conditions == nil {
		tcp.Status.V1Beta2.Conditions = []metav1.Condition{}
	}

	// Available condition: control plane can serve requests
	availableStatus := metav1.ConditionTrue
	if availableCount < desiredReplicas {
		availableStatus = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
		Type:               clusterv1.AvailableCondition,
		Status:             availableStatus,
		ObservedGeneration: tcp.Generation,
		Reason:             "ControlPlaneAvailable",
		Message:            fmt.Sprintf("%d/%d control plane replicas available", availableCount, desiredReplicas),
	})

	// MachinesReady condition: all machines are Ready
	machinesReadyStatus := metav1.ConditionTrue
	if readyCount < desiredReplicas {
		machinesReadyStatus = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
		Type:               clusterv1.MachinesReadyCondition,
		Status:             machinesReadyStatus,
		ObservedGeneration: tcp.Generation,
		Reason:             "MachinesReady",
		Message:            fmt.Sprintf("%d/%d control plane machines are Ready", readyCount, desiredReplicas),
	})

	// MachinesUpToDate condition: no rollout in progress
	machinesUpToDateStatus := metav1.ConditionTrue
	if upToDateCount < desiredReplicas {
		machinesUpToDateStatus = metav1.ConditionFalse
	}
	apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
		Type:               clusterv1.MachinesUpToDateCondition,
		Status:             machinesUpToDateStatus,
		ObservedGeneration: tcp.Generation,
		Reason:             "MachinesUpToDate",
		Message:            fmt.Sprintf("%d/%d control plane machines are up-to-date", upToDateCount, desiredReplicas),
	})
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
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		logger.Error(err, "failed to retrieve control plane machines for cluster")

		return ctrl.Result{}, err
	}

	// --- START OF CORRECTION ---
	// Convert the list of Machines into a list of conditions.Getter
	getters := make([]*clusterv1.Machine, len(ownedMachines.Items))
	for i := range ownedMachines.Items {
		getters[i] = &ownedMachines.Items[i]
	}

	// Aggregate the ReadyCondition from each Machine
	// into the MachinesAllReadyCondition of the TalosControlPlane
	if err := conditions.SetAggregateCondition(
		getters,
		tcp,
		string(clusterv1.ReadyCondition),
		conditions.TargetConditionType(string(controlplanev1.MachinesAllReadyCondition)),
	); err != nil {
		logger.V(4).Info("Failed to set aggregate condition", "error", err)
	}
	// --- END OF CORRECTION ---

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

	if result.RequeueAfter != 0 {
		if err != nil {
			r.Log.Error(err, "reconcile failed", "requeue after", result.RequeueAfter.String(), "error", err.Error())
		}

		return result, nil
	}

	return result, errs
}

// ClusterToTalosControlPlane is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for TalosControlPlane based on updates to a Cluster.
func (r *TalosControlPlaneReconciler) ClusterToTalosControlPlane(_ context.Context, o client.Object) []ctrl.Request {
	c, ok := o.(*clusterv1.Cluster)
	if !ok {
		r.Log.Error(nil, fmt.Sprintf("expected a Cluster but got a %T", o))
		return nil
	}

	controlPlaneRef := c.Spec.ControlPlaneRef
	if controlPlaneRef.IsDefined() && controlPlaneRef.Kind == "TalosControlPlane" {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: c.Namespace, Name: controlPlaneRef.Name}}}
	}

	return nil
}

func (r *TalosControlPlaneReconciler) reconcileDelete(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane) (ctrl.Result, error) {
	// Get list of all control plane machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster))
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

	apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
		Type:    string(controlplanev1.ResizedCondition),
		Status:  metav1.ConditionFalse,
		Reason:  clusterv1.DeletingReason,
		Message: "Deleting TalosControlPlane-owned control plane machines",
	})

	// Requeue the deletion so we can check to make sure machines got cleaned up
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

func (r *TalosControlPlaneReconciler) getControlPlaneMachinesForCluster(ctx context.Context, cluster client.ObjectKey) (clusterv1.MachineList, error) {
	selector := map[string]string{
		clusterv1.ClusterNameLabel:         cluster.Name,
		clusterv1.MachineControlPlaneLabel: "",
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
func (r *TalosControlPlaneReconciler) getFailureDomain(_ context.Context, cluster *clusterv1.Cluster) []string {
	if cluster.Status.FailureDomains == nil {
		return nil
	}

	retList := []string{}
	for _, domain := range cluster.Status.FailureDomains {
		retList = append(retList, domain.Name)
	}
	return retList
}

func (r *TalosControlPlaneReconciler) bootControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, first bool) (ctrl.Result, error) {
	// Since the cloned resource should eventually have a controller ref for the Machine, we create an
	// OwnerReference here without the Controller field set
	infraCloneOwner := &metav1.OwnerReference{
		APIVersion: controlplanev1.GroupVersion.String(),
		Kind:       "TalosControlPlane",
		Name:       tcp.Name,
		UID:        tcp.UID,
	}

	// Clone the infrastructure template
	_, infraRef, err := external.CreateFromTemplate(ctx, &external.CreateFromTemplateInput{
		Client:      r.Client,
		TemplateRef: &tcp.Spec.InfrastructureTemplate,
		Namespace:   tcp.Namespace,
		OwnerRef:    infraCloneOwner,
		ClusterName: cluster.Name,
		Labels: map[string]string{
			clusterv1.MachineControlPlaneLabel: "",
		},
	})
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.InfrastructureTemplateCloningFailedReason,
			Message: fmt.Sprintf("Failed to clone infrastructure template: %v", err),
		})

		return ctrl.Result{}, err
	}

	bootstrapConfig := &tcp.Spec.ControlPlaneConfig.ControlPlaneConfig
	if !reflect.ValueOf(tcp.Spec.ControlPlaneConfig.InitConfig).IsZero() && first {
		bootstrapConfig = &tcp.Spec.ControlPlaneConfig.InitConfig
	}

	// Clone the bootstrap configuration
	bootstrapRef, err := r.generateTalosConfig(ctx, tcp, bootstrapConfig)
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.BootstrapTemplateCloningFailedReason,
			Message: fmt.Sprintf("Failed to create bootstrap configuration: %v", err),
		})

		return ctrl.Result{}, err
	}

	specHash, err := computeSpecHash(tcp)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to compute spec hash")
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.SimpleNameGenerator.GenerateName(tcp.Name + "-"),
			Namespace: tcp.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:         cluster.Name,
				clusterv1.MachineControlPlaneLabel: "",
			},
			Annotations: map[string]string{
				controlplanev1.SpecHashAnnotation: specHash, // ← ajout
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           tcp.Spec.Version,
			InfrastructureRef: infraRef,
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: *bootstrapRef,
			},
		},
	}

	failureDomains := r.getFailureDomain(ctx, cluster)
	if len(failureDomains) > 0 {
		machine.Spec.FailureDomain = failureDomains[rand.Intn(len(failureDomains))]
	}

	if err := r.Client.Create(ctx, machine); err != nil {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.MachineGenerationFailedReason,
			Message: fmt.Sprintf("Failed to create machine: %v", err),
		})

		return ctrl.Result{}, errors.Wrap(err, "Failed to create machine")
	}

	return ctrl.Result{Requeue: true}, nil
}

// reconcileMachineUpToDateConditions poses la condition UpToDate sur chaque Machine
// selon que son SpecHashAnnotation correspond au hash courant du TCP.

func (r *TalosControlPlaneReconciler) bootstrapCluster(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines []clusterv1.Machine) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)

	defer cancel()

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

func (r *TalosControlPlaneReconciler) generateTalosConfig(ctx context.Context, tcp *controlplanev1.TalosControlPlane, spec *cabptv1.TalosConfigSpec) (*clusterv1.ContractVersionedObjectReference, error) {
	owner := metav1.OwnerReference{
		APIVersion:         controlplanev1.GroupVersion.String(),
		Kind:               "TalosControlPlane",
		Name:               tcp.Name,
		UID:                tcp.UID,
		BlockOwnerDeletion: pointer.Bool(true),
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

	bootstrapRef := &clusterv1.ContractVersionedObjectReference{
		APIGroup: cabptv1.GroupVersion.Group,
		Kind:     "TalosConfig",
		Name:     bootstrapConfig.GetName(),
	}

	return bootstrapRef, nil
}
func (r *TalosControlPlaneReconciler) updateStatus(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster) error {
	ensureV1Beta2(tcp)
	clusterSelector := &metav1.LabelSelector{
		MatchLabels: map[string]string{
			clusterv1.ClusterNameLabel:         cluster.Name,
			clusterv1.MachineControlPlaneLabel: "",
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

	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster))
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

	lowestVersion := collections.FromMachineList(&ownedMachines).LowestVersion()
	if lowestVersion != "" {
		tcp.Status.Version = &lowestVersion
	}

	c, err := r.ClusterCache.GetClient(ctx, util.ObjectKey(cluster))
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
	apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
		Type:    string(clusterv1.AvailableCondition),
		Status:  metav1.ConditionTrue,
		Reason:  "ControlPlaneEndpointAvailable",
		Message: "Workload cluster control plane endpoint is available.",
	})

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
	// Build machine pointer slice from owned machines
	machines := make([]*clusterv1.Machine, len(ownedMachines.Items))
	for i := range ownedMachines.Items {
		machines[i] = &ownedMachines.Items[i]
	}
	// Populate v1beta2 replica fields (availableReplicas, upToDateReplicas)
	if err := r.reconcileTalosControlPlaneStatus(ctx, tcp, machines); err != nil {
		r.Log.Error(err, "failed to reconcile v1beta2 status")
	}
	return nil
}

func (r *TalosControlPlaneReconciler) reconcileExternalReference(ctx context.Context, ref corev1.ObjectReference, cluster *clusterv1.Cluster) error {
	if !strings.HasSuffix(ref.Kind, clusterv1.TemplateSuffix) {
		return nil
	}

	obj, err := external.Get(ctx, r.Client, &ref)
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
	ensureV1Beta2(tcp)
	var errs error
	// Audit the etcd member list to remove any nodes that no longer exist
	if err := r.auditEtcd(ctx, tcp, util.ObjectKey(cluster)); err != nil {
		errs = kerrors.NewAggregate([]error{errs, err})
	}

	if err := r.etcdHealthcheck(ctx, tcp, machines.Items); err != nil {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.EtcdClusterHealthyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.EtcdClusterUnhealthyReason,
			Message: fmt.Sprintf("Failed to perform etcd healthcheck: %v", err),
		})

		errs = kerrors.NewAggregate([]error{errs, err})
	} else {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.EtcdClusterHealthyCondition),
			Status:  metav1.ConditionTrue,
			Reason:  controlplanev1.EtcdClusterHealthyReason,
			Message: fmt.Sprintf("ETCD healthcheck successfull"),
		})
	}

	if errs != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, errs
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileNodeHealth(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if err := r.nodesHealthcheck(ctx, tcp, machines.Items); err != nil {
		reason := controlplanev1.ControlPlaneComponentsInspectionFailedReason

		if errors.Is(err, &errServiceUnhealthy{}) {
			reason = controlplanev1.ControlPlaneComponentsUnhealthyReason
		}

		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.ControlPlaneComponentsHealthyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: fmt.Sprintf("Failed to perform control plane healthcheck: %v", err),
		})

		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	} else {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.ControlPlaneComponentsHealthyCondition),
			Status:  metav1.ConditionTrue,
			Reason:  "ControlPlaneComponentsHealthy",
			Message: "Control plane components are healthy",
		})
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileConditions(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if !conditions.Has(tcp, string(clusterv1.AvailableCondition)) {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(clusterv1.AvailableCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.WaitingForTalosBootReason,
			Message: "Waiting for Talos to bootstrap",
		})
	}

	if !conditions.Has(tcp, string(controlplanev1.MachinesBootstrapped)) {
		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesBootstrapped),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.WaitingForMachinesReason,
			Message: "Waiting for machines to bootstrap",
		})
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileMachines(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (res ctrl.Result, err error) {
	logger := r.Log.WithValues("namespace", tcp.Namespace, "talosControlPlane", tcp.Name)

	// If we've made it this far, we can assume that all ownedMachines are up to date
	numMachines := len(machines.Items)
	desiredReplicas := int(tcp.Spec.GetReplicas())

	controlPlane, err := newControlPlane(ctx, r.Client, cluster, tcp, collections.FromMachineList(machines))
	if err != nil {
		return ctrl.Result{}, err
	}

	needRollout := controlPlane.MachinesNeedingRollout()
	if len(needRollout) > 0 {
		logger.Info("rolling out control plane machines", "needRollout", needRollout.Names())
		apimeta.SetStatusCondition(&controlPlane.TCP.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesSpecUpToDateCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.RollingUpdateInProgressReason,
			Message: fmt.Sprintf("Rolling %d replicas with outdated spec (%d replicas up to date)", len(needRollout), len(controlPlane.Machines)-len(needRollout)),
		})

		return r.upgradeControlPlane(ctx, cluster, tcp, controlPlane, needRollout)
	} else {
		if conditions.Has(controlPlane.TCP, string(controlplanev1.MachinesSpecUpToDateCondition)) {
			apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesSpecUpToDateCondition),
				Status:  metav1.ConditionTrue,
				Reason:  "MachinesSpecUpToDate",
				Message: "All control plane machines have up-to-date spec",
			})

		}
	}

	switch {
	// We are creating the first replica
	case numMachines < desiredReplicas && numMachines == 0:
		// Create new Machine w/ init
		logger.Info("initializing control plane", "Desired", desiredReplicas, "Existing", numMachines)

		return r.bootControlPlane(ctx, cluster, tcp, true)
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

			apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesBootstrapped),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.MachinesBootstrappedReason,
				Message: fmt.Sprintf("Control plane bootstrapped successfully"),
			})
		}

		if !tcp.Status.Bootstrapped {
			if err := r.bootstrapCluster(ctx, tcp, machines.Items); err != nil {
				apimeta.SetStatusCondition(&controlPlane.TCP.Status.V1Beta2.Conditions, metav1.Condition{
					Type:    string(controlplanev1.MachinesBootstrapped),
					Status:  metav1.ConditionFalse,
					Reason:  controlplanev1.WaitingForTalosBootReason,
					Message: fmt.Sprintf("Failed to bootstrap cluster: %v", err),
				})

				logger.Info("bootstrap failed, retrying in 20 seconds", "error", err)

				return ctrl.Result{RequeueAfter: time.Second * 20}, nil
			}

			apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesBootstrapped),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.MachinesBootstrappedReason,
				Message: fmt.Sprintf("Control plane bootstrapped successfully"),
			})

			tcp.Status.Bootstrapped = true
		}

		if conditions.Has(tcp, string(controlplanev1.MachinesAllReadyCondition)) {
			apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
				Type:    string(controlplanev1.ResizedCondition),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.ResizedReason,
				Message: fmt.Sprintf("ControlPlade successfully resized"),
			})
		}

		apimeta.SetStatusCondition(&tcp.Status.V1Beta2.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionTrue,
			Reason:  controlplanev1.MachinesCreatedReason,
			Message: fmt.Sprintf("Machine has beens succesfully created"),
		})
	}

	return ctrl.Result{}, nil
}

func patchTalosControlPlane(ctx context.Context, patchHelper *patch.Helper, tcp *controlplanev1.TalosControlPlane, opts ...patch.Option) error {
	// Always update the readyCondition by summarizing the state of other conditions.
	err := conditions.SetSummaryCondition(
		tcp,
		tcp,
		clusterv1.ReadyCondition,
		conditions.ForConditionTypes{
			string(controlplanev1.MachinesCreatedCondition),
			string(controlplanev1.ResizedCondition),
			string(controlplanev1.MachinesAllReadyCondition),
			string(clusterv1.AvailableCondition),
			string(controlplanev1.MachinesBootstrapped),
		},
	)
	if err != nil {
		return errors.Wrap(err, "failed to set summary Ready condition")
	}

	opts = append(opts,
		patch.WithOwnedConditions{Conditions: []string{
			string(controlplanev1.MachinesCreatedCondition),
			string(clusterv1.ReadyCondition),
			string(controlplanev1.ResizedCondition),
			string(controlplanev1.MachinesAllReadyCondition),
			string(clusterv1.AvailableCondition),
			string(controlplanev1.MachinesBootstrapped),
		}},
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		ctx,
		tcp,
		opts...,
	)
}

// computeSpecHash returns a stable FNV-32a hash of the fields that determine
// whether a machine is up-to-date with the current TalosControlPlane spec.
func computeSpecHash(tcp *controlplanev1.TalosControlPlane) (string, error) {
	type specKey struct {
		Version            string
		ControlPlaneConfig controlplanev1.ControlPlaneConfig
	}
	data, err := json.Marshal(specKey{
		Version:            tcp.Spec.Version,
		ControlPlaneConfig: tcp.Spec.ControlPlaneConfig,
	})
	if err != nil {
		return "", err
	}
	h := fnv.New32a()
	if _, err := h.Write(data); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", h.Sum32()), nil
}
