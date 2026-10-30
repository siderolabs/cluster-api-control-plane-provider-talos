// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/pointer"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/labels/format"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

const requeueDuration = 30 * time.Second

// remoteConnectionGracePeriod is how long the Available condition keeps its last known state while the workload
// cluster can't be reached, e.g. right after a controller restart, before it is set to Unknown. It is the default
// remote conditions grace period of KubeadmControlPlane.
const remoteConnectionGracePeriod = 5 * time.Minute

// TalosControlPlaneReconciler reconciles a TalosControlPlane object
type TalosControlPlaneReconciler struct {
	client.Client
	APIReader    client.Reader
	Log          logr.Logger
	Scheme       *runtime.Scheme
	ClusterCache clustercache.ClusterCache

	// etcd runs etcd operations on individual machines; nil means through the Talos API.
	etcd etcdMemberManager
}

func (r *TalosControlPlaneReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&controlplanev1.TalosControlPlane{}).
		Owns(&clusterv1.Machine{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.ClusterToTalosControlPlane),
		).
		// Enqueue the TalosControlPlane when the connection to the workload cluster goes up or down.
		// Also enqueue it when the health probe keeps failing, so that Available goes Unknown once the grace period expired.
		WatchesRawSource(r.ClusterCache.GetClusterSource("taloscontrolplane", r.ClusterToTalosControlPlane,
			clustercache.WatchForProbeFailure(remoteConnectionGracePeriod))).
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
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

func (r *TalosControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, reterr error) {
	logger := r.Log.WithValues("namespace", req.Namespace, "talosControlPlane", req.Name)

	// Fetch the TalosControlPlane instance.
	tcp := &controlplanev1.TalosControlPlane{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, tcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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
		return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	// Persist the fixed-up condition reasons before any other status patch. The patch helper applies condition
	// changes on top of the stored conditions, so as long as a stored condition has an empty reason, every status
	// patch, starting with the Paused condition below, would be rejected.
	if hasEmptyConditionReason(tcp) {
		patchHelper, err := patch.NewHelper(tcp, r.Client)
		if err != nil {
			return ctrl.Result{}, err
		}

		sanitizeConditionReasons(tcp)

		if err := patchHelper.Patch(ctx, tcp); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to fix up empty condition reasons")
		}
	}

	// Initialize the patch helper before the Paused condition is surfaced: when only the observed generation of the
	// condition changed, EnsurePausedCondition sets it without patching, leaving it to the patch at the end of the
	// reconcile, which only sends what changed since the helper was created.
	patchHelper, err := patch.NewHelper(tcp, r.Client)
	if err != nil {
		logger.Error(err, "failed to configure the patch helper")
		return ctrl.Result{}, err
	}

	// Surface the Paused condition. When the condition was just patched, reconciliation continues
	// right away with the updated object instead of requeueing.
	isPaused, _, err := paused.EnsurePausedCondition(ctx, r.Client, cluster, tcp)
	if err != nil {
		return ctrl.Result{}, err
	}

	if isPaused {
		// The Cluster and TalosControlPlane watches enqueue the object again once it is unpaused.
		logger.Info("reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if ensureTalosControlPlaneFinalizers(tcp) {
		// patch and return right away instead of reusing the main defer,
		// because the main defer may take too much time to get cluster status

		if err := patchTalosControlPlane(ctx, patchHelper, tcp, patch.WithStatusObservedGeneration{}); err != nil {
			logger.Error(err, "failed to add finalizer to TalosControlPlane")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// The ControlPlane built by reconcile, reused by the status update; nil if reconcile did not build it.
	var controlPlane *ControlPlane

	defer func() {
		r.Log.Info("attempting to set control plane status")

		// Always attempt to update status.
		if err := r.updateStatus(ctx, tcp, cluster, controlPlane); err != nil {
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
		if reterr == nil && res.IsZero() && tcp.ObjectMeta.DeletionTimestamp.IsZero() {
			deprecated := tcp.V1Beta1DeprecatedStatus()
			if !deprecated.Ready || deprecated.UnavailableReplicas > 0 {
				res = ctrl.Result{RequeueAfter: 20 * time.Second}
			}
		}

		logger.Info("successfully updated control plane status")
	}()

	if !tcp.ObjectMeta.DeletionTimestamp.IsZero() {
		// Handle deletion reconciliation loop.
		return r.reconcileDelete(ctx, cluster, tcp)
	}

	res, controlPlane, reterr = r.reconcile(ctx, cluster, tcp)

	return res, reterr
}

// reconcile runs the reconcile steps of a TalosControlPlane that is not being deleted. It also returns the
// ControlPlane it built, if any, so that the status update reuses it instead of fetching the same objects again.
func (r *TalosControlPlaneReconciler) reconcile(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane) (ctrl.Result, *ControlPlane, error) {
	logger := ctrl.LoggerFrom(ctx, "cluster", cluster.Name)
	logger.Info("reconcile TalosControlPlane")

	// Wait for the cluster infrastructure to be provisioned before creating machines.
	// This is the contract field, which is never reset once set, unlike the InfrastructureReady condition
	// that mirrors the live state of the InfraCluster.
	if !ptr.Deref(cluster.Status.Initialization.InfrastructureProvisioned, false) {
		logger.Info("cluster infrastructure is not provisioned yet")

		return ctrl.Result{}, nil, nil
	}

	// Update ownerrefs on infra templates
	if err := r.reconcileExternalReference(ctx, tcp.Spec.MachineTemplate.Spec.InfrastructureRef, cluster); err != nil {
		// A missing template is a user-recoverable misconfiguration; back off instead of
		// returning the error so the controller does not hot-loop.
		if apierrors.IsNotFound(err) {
			apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesCreatedCondition),
				Status:  metav1.ConditionFalse,
				Reason:  controlplanev1.InfrastructureTemplateCloningFailedReason,
				Message: fmt.Sprintf("Failed to retrieve infrastructure template: %v", err),
			})

			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil, nil
		}

		return ctrl.Result{}, nil, err
	}

	// If ControlPlaneEndpoint is not set, return early
	if !cluster.Spec.ControlPlaneEndpoint.IsValid() {
		logger.Info("cluster does not yet have a ControlPlaneEndpoint defined")

		return ctrl.Result{}, nil, nil
	}

	// TODO: handle proper adoption of Machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster))
	if err != nil {
		logger.Error(err, "failed to retrieve control plane machines for cluster")

		return ctrl.Result{}, nil, err
	}

	if err := r.reconcileMachineTemplateState(ctx, cluster, tcp, &ownedMachines); err != nil {
		return ctrl.Result{}, nil, err
	}

	getters := make([]*clusterv1.Machine, len(ownedMachines.Items))
	for i := range ownedMachines.Items {
		getters[i] = &ownedMachines.Items[i]
	}

	// Aggregate the Ready condition of each Machine into the MachinesAllReady condition of the TalosControlPlane.
	if err := conditions.SetAggregateCondition(
		getters,
		tcp,
		string(clusterv1.ReadyCondition),
		conditions.TargetConditionType(string(controlplanev1.MachinesAllReadyCondition)),
	); err != nil {
		logger.V(4).Info("Failed to set aggregate condition", "error", err)
	}

	// Build the ControlPlane once: the rollout and the status update both need the infrastructure machines and
	// TalosConfigs of the Machines. If it fails, the steps that don't need it still run, and the steps that do
	// try again.
	controlPlane, err := newControlPlane(ctx, r.Client, cluster, tcp, collections.FromMachineList(&ownedMachines))
	if err != nil {
		logger.Info("failed to build the control plane", "error", err)

		controlPlane = nil
	}

	var (
		errs        error
		result      ctrl.Result
		phaseResult ctrl.Result

		// etcdStatuses is what the etcd health check observed, so that remediation doesn't inspect etcd again.
		etcdStatuses map[string]etcdMachineStatus
	)

	// run all similar reconcile steps in the loop and pick the lowest RetryAfter, aggregate errors and check the requeue flags.
	// Remediation and scaling both delete Machines, so they run together as the last step, which deletes at most one Machine.
	for _, phase := range []func(context.Context, *clusterv1.Cluster, *controlplanev1.TalosControlPlane, *clusterv1.MachineList) (ctrl.Result, error){
		func(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (ctrl.Result, error) {
			var result ctrl.Result

			result, etcdStatuses, err = r.reconcileEtcdMembers(ctx, cluster, tcp, machines)

			return result, err
		},
		r.reconcileNodeHealth,
		r.reconcileConditions,
		r.reconcileKubeconfig,
		func(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (ctrl.Result, error) {
			return r.reconcileRemediationAndMachines(ctx, cluster, tcp, machines, controlPlane, etcdStatuses)
		},
	} {
		phaseResult, err = phase(ctx, cluster, tcp, &ownedMachines)
		if err != nil {
			errs = kerrors.NewAggregate([]error{errs, err})
		}

		result = util.LowestNonZeroResult(result, phaseResult)
	}

	// The ClusterCache source enqueues the TalosControlPlane again once the workload cluster is reachable,
	// so a lost connection doesn't need to go through the error backoff.
	if errs != nil && errors.Is(errs, clustercache.ErrClusterNotConnected) {
		result = util.LowestNonZeroResult(result, ctrl.Result{RequeueAfter: requeueDuration})
	}

	if result.RequeueAfter != 0 {
		if errs != nil {
			r.Log.Error(errs, "reconcile failed", "requeue after", result.RequeueAfter.String())
		}

		return result, controlPlane, nil
	}

	return result, controlPlane, errs
}

// reconcileMachineTemplateState propagates the in-place mutable fields of the TalosControlPlane to its Machines,
// without rolling them out.
func (r *TalosControlPlaneReconciler) reconcileMachineTemplateState(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) error {
	desiredLabels := controlPlaneMachineLabelsForCluster(tcp, cluster.Name)
	desiredAnnotations := copyStringMap(tcp.Spec.MachineTemplate.ObjectMeta.Annotations)
	desiredReadinessGates := copyMachineReadinessGates(tcp.Spec.MachineTemplate.Spec.ReadinessGates)
	desiredDeletionSpec := buildMachineDeletionSpec(tcp.Spec.MachineTemplate.Spec.Deletion)

	for i := range machines.Items {
		machine := &machines.Items[i]
		if !machine.DeletionTimestamp.IsZero() {
			continue
		}

		patchHelper, err := patch.NewHelper(machine, r.Client)
		if err != nil {
			return err
		}

		changed := false

		if machine.Labels == nil {
			machine.Labels = map[string]string{}
		}

		for key, value := range desiredLabels {
			if machine.Labels[key] != value {
				machine.Labels[key] = value
				changed = true
			}
		}

		if len(desiredAnnotations) > 0 && machine.Annotations == nil {
			machine.Annotations = map[string]string{}
		}

		for key, value := range desiredAnnotations {
			if machine.Annotations[key] != value {
				machine.Annotations[key] = value
				changed = true
			}
		}

		if !reflect.DeepEqual(machine.Spec.ReadinessGates, desiredReadinessGates) {
			machine.Spec.ReadinessGates = copyMachineReadinessGates(desiredReadinessGates)
			changed = true
		}

		if !reflect.DeepEqual(machine.Spec.Deletion, desiredDeletionSpec) {
			machine.Spec.Deletion = desiredDeletionSpec
			changed = true
		}

		if !ptr.Equal(machine.Spec.MinReadySeconds, tcp.Spec.MinReadySeconds) {
			machine.Spec.MinReadySeconds = copyInt32(tcp.Spec.MinReadySeconds)
			changed = true
		}

		if !changed {
			continue
		}

		if err := patchHelper.Patch(ctx, machine); err != nil {
			return errors.Wrapf(err, "failed to sync machine %s with the machine template", machine.Name)
		}
	}

	return nil
}

func controlPlaneMachineLabelsForCluster(tcp *controlplanev1.TalosControlPlane, clusterName string) map[string]string {
	labels := copyStringMap(tcp.Spec.MachineTemplate.ObjectMeta.Labels)
	if labels == nil {
		labels = map[string]string{}
	}

	labels[clusterv1.ClusterNameLabel] = clusterName
	labels[clusterv1.MachineControlPlaneLabel] = ""
	labels[clusterv1.MachineControlPlaneNameLabel] = format.MustFormatValue(tcp.Name)

	return labels
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}

	return out
}

func copyMachineReadinessGates(in []clusterv1.MachineReadinessGate) []clusterv1.MachineReadinessGate {
	if len(in) == 0 {
		return nil
	}

	out := make([]clusterv1.MachineReadinessGate, len(in))
	copy(out, in)

	return out
}

func buildMachineDeletionSpec(deletion controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec) clusterv1.MachineDeletionSpec {
	return clusterv1.MachineDeletionSpec{
		NodeDrainTimeoutSeconds:        copyInt32(deletion.NodeDrainTimeoutSeconds),
		NodeVolumeDetachTimeoutSeconds: copyInt32(deletion.NodeVolumeDetachTimeoutSeconds),
		NodeDeletionTimeoutSeconds:     copyInt32(deletion.NodeDeletionTimeoutSeconds),
	}
}

func copyInt32(v *int32) *int32 {
	if v == nil {
		return nil
	}

	return ptr.To(*v)
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
	if controlPlaneRef.IsDefined() &&
		controlPlaneRef.APIGroup == controlplanev1.GroupVersion.Group &&
		controlPlaneRef.Kind == "TalosControlPlane" {
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

	// If no control plane machines remain, remove the finalizer.
	// The deferred patch persists it: updating the object here would delete it before that patch runs, which then fails.
	if len(ownedMachines.Items) == 0 {
		removeTalosControlPlaneFinalizers(tcp)
		return ctrl.Result{}, nil
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

	apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
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

func ensureTalosControlPlaneFinalizers(tcp *controlplanev1.TalosControlPlane) bool {
	// The API server rejects new finalizers on an object that is being deleted, so a TalosControlPlane
	// that still carries the legacy finalizer keeps it until reconcileDelete removes both.
	if !tcp.DeletionTimestamp.IsZero() {
		return false
	}

	changed := false

	if !controllerutil.ContainsFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer) {
		controllerutil.AddFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)
		changed = true
	}

	if controllerutil.ContainsFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizerLegacy) {
		controllerutil.RemoveFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizerLegacy)
		changed = true
	}

	return changed
}

func removeTalosControlPlaneFinalizers(tcp *controlplanev1.TalosControlPlane) {
	controllerutil.RemoveFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizer)
	controllerutil.RemoveFinalizer(tcp, controlplanev1.TalosControlPlaneFinalizerLegacy)
}

// logFailureDomainSelection reports where a new control plane machine is about to be placed, and
// whether the cluster offers enough failure domains to spread the control plane at all.
//
// A control plane which cannot be spread comes from how the infrastructure provider populates the
// cluster failure domains rather than from this controller, and it is next to impossible to
// diagnose from the outside, so it is worth a log line on every machine creation.
func (r *TalosControlPlaneReconciler) logFailureDomainSelection(tcp *controlplanev1.TalosControlPlane, controlPlane *ControlPlane, machine *clusterv1.Machine) {
	logger := r.Log.WithValues("namespace", tcp.Namespace, "talosControlPlane", tcp.Name, "machine", machine.Name)

	if machine.Spec.FailureDomain == "" {
		logger.Info("cluster reports no failure domains, control plane machines will not be spread")

		return
	}

	eligible := failureDomainNames(controlPlane.FailureDomains())

	logger = logger.WithValues("failureDomain", machine.Spec.FailureDomain, "eligibleFailureDomains", eligible)

	logger.Info("picked failure domain for the new control plane machine")

	markedForControlPlane := slices.ContainsFunc(controlPlane.Cluster.Status.FailureDomains, func(failureDomain clusterv1.FailureDomain) bool {
		return ptr.Deref(failureDomain.ControlPlane, false)
	})
	if !markedForControlPlane {
		logger.Info("no failure domain is marked for the control plane, falling back to all cluster failure domains")
	}

	if desiredReplicas := int(tcp.Spec.GetReplicas()); len(eligible) < desiredReplicas {
		logger.Info("fewer control plane failure domains than desired replicas, the control plane cannot be fully spread",
			"desiredReplicas", desiredReplicas)
	}
}

func (r *TalosControlPlaneReconciler) bootControlPlane(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane,
	controlPlane *ControlPlane, first bool,
) (ctrl.Result, error) {
	// Since the cloned resource should eventually have a controller ref for the Machine, we create an
	// OwnerReference here without the Controller field set
	infraCloneOwner := &metav1.OwnerReference{
		APIVersion: controlplanev1.GroupVersion.String(),
		Kind:       "TalosControlPlane",
		Name:       tcp.Name,
		UID:        tcp.UID,
	}

	machineName, err := tcp.Spec.GenerateMachineName(cluster.Name, tcp.Name)
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.MachineGenerationFailedReason,
			Message: fmt.Sprintf("Failed to generate machine name: %v", err),
		})

		return ctrl.Result{}, err
	}

	// Clone the infrastructure template. The infrastructureRef is a ContractVersionedObjectReference,
	// so the concrete apiVersion is resolved through the contract labels of the template CRD.
	templateObj, err := external.GetObjectFromContractVersionedRef(ctx, r.Client, tcp.Spec.MachineTemplate.Spec.InfrastructureRef, tcp.Namespace)
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.InfrastructureTemplateCloningFailedReason,
			Message: fmt.Sprintf("Failed to retrieve infrastructure template: %v", err),
		})

		// A missing template is a user-recoverable misconfiguration; back off instead of
		// returning the error so the controller does not hot-loop.
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		return ctrl.Result{}, err
	}

	templateRef := corev1.ObjectReference{
		APIVersion: templateObj.GetAPIVersion(),
		Kind:       templateObj.GetKind(),
		Name:       templateObj.GetName(),
		Namespace:  templateObj.GetNamespace(),
	}

	_, infraRef, err := external.CreateFromTemplate(ctx, &external.CreateFromTemplateInput{
		Client:      r.Client,
		TemplateRef: &templateRef,
		Namespace:   tcp.Namespace,
		Name:        machineName,
		OwnerRef:    infraCloneOwner,
		ClusterName: cluster.Name,
		Labels: map[string]string{
			clusterv1.MachineControlPlaneLabel: "",
		},
	})
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
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
	bootstrapRef, err := r.generateTalosConfig(ctx, tcp, machineName, bootstrapConfig)
	if err != nil {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.BootstrapTemplateCloningFailedReason,
			Message: fmt.Sprintf("Failed to create bootstrap configuration: %v", err),
		})

		return ctrl.Result{}, err
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        machineName,
			Namespace:   tcp.Namespace,
			Labels:      controlPlaneMachineLabelsForCluster(tcp, cluster.Name),
			Annotations: copyStringMap(tcp.Spec.MachineTemplate.ObjectMeta.Annotations),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tcp, controlplanev1.GroupVersion.WithKind("TalosControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           tcp.Spec.Version,
			InfrastructureRef: infraRef,
			ReadinessGates:    copyMachineReadinessGates(tcp.Spec.MachineTemplate.Spec.ReadinessGates),
			Deletion:          buildMachineDeletionSpec(tcp.Spec.MachineTemplate.Spec.Deletion),
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: *bootstrapRef,
			},
			MinReadySeconds: copyInt32(tcp.Spec.MinReadySeconds),
		},
	}

	// A Machine created while a remediation is in progress replaces the remediated Machine: record the remediation
	// on it right away, so that its retries are counted even if the remediation completes without it.
	if value, ok := tcp.Annotations[remediationInProgressAnnotation]; ok {
		if machine.Annotations == nil {
			machine.Annotations = map[string]string{}
		}

		machine.Annotations[remediationForAnnotation] = value
	}

	machine.Spec.FailureDomain = controlPlane.NextFailureDomainForScaleUp(ctx)

	r.logFailureDomainSelection(tcp, controlPlane, machine)

	if err := r.Client.Create(ctx, machine); err != nil {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.MachineGenerationFailedReason,
			Message: fmt.Sprintf("Failed to create machine: %v", err),
		})

		return ctrl.Result{}, errors.Wrap(err, "Failed to create machine")
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

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

func (r *TalosControlPlaneReconciler) generateTalosConfig(ctx context.Context, tcp *controlplanev1.TalosControlPlane, name string, spec *cabptv1.TalosConfigSpec) (*clusterv1.ContractVersionedObjectReference, error) {
	owner := metav1.OwnerReference{
		APIVersion:         controlplanev1.GroupVersion.String(),
		Kind:               "TalosControlPlane",
		Name:               tcp.Name,
		UID:                tcp.UID,
		BlockOwnerDeletion: pointer.Bool(true),
	}

	bootstrapConfig := &cabptv1.TalosConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
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

// updateStatus computes the status of the TalosControlPlane. controlPlane is the ControlPlane built by the reconcile,
// or nil if it built none.
func (r *TalosControlPlaneReconciler) updateStatus(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster, controlPlane *ControlPlane) error {
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

	machines := collections.FromMachineList(&ownedMachines)

	lowestVersion := machines.LowestVersion()
	if lowestVersion != "" {
		tcp.Status.Version = &lowestVersion
	}

	// Everything up to the workload cluster access below only uses the Machines in the management cluster,
	// so it stays accurate when the workload cluster is unreachable or the TalosControlPlane is being deleted.
	var errs []error

	// While the TalosControlPlane is being deleted, its Machines are not rolled out anymore, so their UpToDate
	// condition is left as is, rather than fetching infrastructure machines that may be gone or going.
	if tcp.DeletionTimestamp.IsZero() {
		if err := r.reconcileMachineUpToDateConditions(ctx, cluster, tcp, machines, controlPlane); err != nil {
			errs = append(errs, err)
		}
	}

	var readyReplicas, availableReplicas, upToDateReplicas, machinesWithNode int32

	for _, machine := range machines {
		if machine.Status.NodeRef.IsDefined() {
			machinesWithNode++
		}

		if conditions.IsTrue(machine, clusterv1.MachineReadyCondition) {
			readyReplicas++
		}

		if conditions.IsTrue(machine, clusterv1.MachineAvailableCondition) {
			availableReplicas++
		}

		if conditions.IsTrue(machine, clusterv1.MachineUpToDateCondition) {
			upToDateReplicas++
		}
	}

	replicas := int32(len(machines))

	tcp.Status.Replicas = ptr.To(replicas)
	tcp.Status.ReadyReplicas = ptr.To(readyReplicas)
	tcp.Status.AvailableReplicas = ptr.To(availableReplicas)
	tcp.Status.UpToDateReplicas = ptr.To(upToDateReplicas)

	// v1beta1 contract fields, derived from the same counts.
	deprecated := tcp.V1Beta1DeprecatedStatus()
	deprecated.UnavailableReplicas = replicas - readyReplicas
	deprecated.UpdatedReplicas = upToDateReplicas
	deprecated.Ready = readyReplicas > 0

	setMachinesReadyCondition(tcp, readyReplicas)
	setMachinesUpToDateCondition(tcp, replicas, upToDateReplicas)

	// Don't connect to the workload cluster while the TalosControlPlane is being deleted.
	var workloadErr error

	if tcp.DeletionTimestamp.IsZero() {
		workloadErr = r.reconcileControlPlaneInitialized(ctx, tcp, cluster)
		if workloadErr != nil {
			r.Log.Info("failed to reach the workload cluster", "error", workloadErr)
		}
	}

	setAvailableCondition(tcp, machinesWithNode, workloadErr, workloadErr != nil && r.withinRemoteConnectionGracePeriod(ctx, cluster))

	r.Log.Info("ready replicas", "count", readyReplicas)

	return kerrors.NewAggregate(errs)
}

// reconcileControlPlaneInitialized marks the control plane as initialized once the workload cluster API
// can be reached. It returns an error if the workload cluster can't be reached.
func (r *TalosControlPlaneReconciler) reconcileControlPlaneInitialized(ctx context.Context, tcp *controlplanev1.TalosControlPlane, cluster *clusterv1.Cluster) error {
	c, err := r.ClusterCache.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		return errors.Wrap(err, "failed to get a client for the workload cluster")
	}

	nodeSelector := labels.NewSelector()
	req, err := labels.NewRequirement(constants.LabelNodeRoleControlPlane, selection.Exists, []string{})
	if err != nil {
		return err
	}

	var nodes v1.NodeList

	if err := c.List(ctx, &nodes, &client.ListOptions{
		LabelSelector: nodeSelector.Add(*req),
	}); err != nil {
		return errors.Wrap(err, "failed to list control plane nodes")
	}

	// if we were able to fetch some resources via control plane endpoint,
	// workload cluster control plane endpoint is available
	tcp.V1Beta1DeprecatedStatus().Initialized = true
	// the Cluster controller reads this v1beta2 field (not the legacy status.initialized
	// bool above) to compute Cluster.Status.Conditions[ControlPlaneInitialized]; without it,
	// that condition never flips true and Machines stay stuck waiting on it.
	tcp.Status.Initialization.ControlPlaneInitialized = ptr.To(true)

	return nil
}

// withinRemoteConnectionGracePeriod reports whether a failure to reach the workload cluster is still within the
// grace period: the ClusterCache did not try to connect often enough yet, e.g. right after a controller restart,
// or the last successful health probe is more recent than remoteConnectionGracePeriod. It mirrors how
// KubeadmControlPlane keeps its remote conditions.
func (r *TalosControlPlaneReconciler) withinRemoteConnectionGracePeriod(ctx context.Context, cluster *clusterv1.Cluster) bool {
	state := r.ClusterCache.GetHealthCheckingState(ctx, util.ObjectKey(cluster))

	if state.LastProbeSuccessTime.IsZero() {
		return state.ConsecutiveFailures < 5
	}

	return time.Since(state.LastProbeSuccessTime) <= remoteConnectionGracePeriod
}

// reconcileMachineUpToDateConditions sets the UpToDate condition on each Machine, using the same predicate
// that selects the Machines to roll out. Only Machines whose condition changed are patched.
//
// controlPlane is the ControlPlane built earlier in the reconcile, if any. It may miss Machines created since, which
// were created from the current spec: their infrastructure machine and TalosConfig are not known to it, so they
// are only compared by Kubernetes version, as Machines whose infrastructure machine or TalosConfig can't be fetched.
func (r *TalosControlPlaneReconciler) reconcileMachineUpToDateConditions(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	machines collections.Machines,
	controlPlane *ControlPlane,
) error {
	if controlPlane == nil {
		var err error

		controlPlane, err = newControlPlane(ctx, r.Client, cluster, tcp, machines)
		if err != nil {
			return errors.Wrap(err, "failed to compute machine UpToDate conditions")
		}
	}

	matchesSpec := controlPlane.MatchesSpec()

	var errs []error

	for _, machine := range machines {
		condition := metav1.Condition{
			Type:               clusterv1.MachineUpToDateCondition,
			Status:             metav1.ConditionTrue,
			Reason:             clusterv1.MachineUpToDateReason,
			ObservedGeneration: machine.Generation,
		}

		if !matchesSpec(machine) {
			condition.Status = metav1.ConditionFalse
			condition.Reason = clusterv1.MachineNotUpToDateReason
			condition.Message = "Machine does not match the TalosControlPlane version, infrastructure template or controlplane config"
		}

		if current := conditions.Get(machine, clusterv1.MachineUpToDateCondition); current != nil && conditions.HasSameState(current, &condition) {
			continue
		}

		patchHelper, err := patch.NewHelper(machine, r.Client)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		conditions.Set(machine, condition)

		if err := patchHelper.Patch(ctx, machine, patch.WithOwnedConditions{Conditions: []string{
			clusterv1.MachineUpToDateCondition,
		}}); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to patch UpToDate condition on machine %s", machine.Name))
		}
	}

	return kerrors.NewAggregate(errs)
}

// setMachinesReadyCondition reports whether at least spec.replicas control plane machines are ready.
func setMachinesReadyCondition(tcp *controlplanev1.TalosControlPlane, readyReplicas int32) {
	desiredReplicas := tcp.Spec.GetReplicas()

	condition := metav1.Condition{
		Type:    clusterv1.MachinesReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  controlplanev1.MachinesReadyReason,
		Message: fmt.Sprintf("%d of %d control plane machines are ready", readyReplicas, desiredReplicas),
	}

	if readyReplicas < desiredReplicas {
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.MachinesNotReadyReason
	}

	conditions.Set(tcp, condition)
}

// setMachinesUpToDateCondition reports whether all control plane machines are up-to-date.
func setMachinesUpToDateCondition(tcp *controlplanev1.TalosControlPlane, replicas, upToDateReplicas int32) {
	condition := metav1.Condition{
		Type:    clusterv1.MachinesUpToDateCondition,
		Status:  metav1.ConditionTrue,
		Reason:  controlplanev1.MachinesUpToDateReason,
		Message: fmt.Sprintf("%d of %d control plane machines are up-to-date", upToDateReplicas, replicas),
	}

	if upToDateReplicas < replicas {
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.MachinesNotUpToDateReason
	}

	conditions.Set(tcp, condition)
}

// setAvailableCondition is the only place that sets the Available condition.
//
// The control plane is available when it is initialized, the workload cluster can be reached, etcd has quorum and
// at least one control plane machine has a node. Unlike the Machine Ready condition, this doesn't depend on the Node
// Ready condition: Nodes stay NotReady until a CNI is installed, while the control plane already serves, which is
// also how KubeadmControlPlane computes Available. Having fewer ready machines than spec.replicas, e.g. during a
// rollout, is reported by the MachinesReady condition instead.
//
// When the workload cluster can't be reached, the last known state is kept during the remote connection grace
// period (keepOnWorkloadErr), so that a controller restart or a short connection loss doesn't flip the condition.
func setAvailableCondition(tcp *controlplanev1.TalosControlPlane, machinesWithNode int32, workloadErr error, keepOnWorkloadErr bool) {
	condition := metav1.Condition{
		Type:   clusterv1.AvailableCondition,
		Status: metav1.ConditionTrue,
		Reason: controlplanev1.AvailableReason,
	}

	etcdHealthy := conditions.Get(tcp, string(controlplanev1.EtcdClusterHealthyCondition))
	etcdUnhealthy := etcdHealthy != nil && etcdHealthy.Status == metav1.ConditionFalse

	switch {
	case !tcp.DeletionTimestamp.IsZero():
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.NotAvailableReason
		condition.Message = "TalosControlPlane is being deleted"
	case !ptr.Deref(tcp.Status.Initialization.ControlPlaneInitialized, false):
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.NotAvailableReason
		condition.Message = "Control plane is not yet initialized"
	case workloadErr != nil && keepOnWorkloadErr && conditions.Has(tcp, clusterv1.AvailableCondition):
		return
	case workloadErr != nil:
		condition.Status = metav1.ConditionUnknown
		condition.Reason = controlplanev1.RemoteConnectionProbeFailedReason
		condition.Message = fmt.Sprintf("Workload cluster can't be reached: %v", workloadErr)
	case etcdUnhealthy && etcdHealthy.Reason == controlplanev1.EtcdClusterQuorumLostReason:
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.NotAvailableReason
		condition.Message = fmt.Sprintf("etcd does not have quorum: %s", etcdHealthy.Message)
	case etcdUnhealthy && etcdHealthy.Reason == controlplanev1.EtcdClusterInspectionFailedReason:
		condition.Status = metav1.ConditionUnknown
		condition.Reason = controlplanev1.AvailableInspectionFailedReason
		condition.Message = fmt.Sprintf("etcd can't be inspected: %s", etcdHealthy.Message)
	case machinesWithNode < 1:
		condition.Status = metav1.ConditionFalse
		condition.Reason = controlplanev1.NotAvailableReason
		condition.Message = "There are no control plane machines with a node, at least 1 required"
	case etcdUnhealthy:
		condition.Message = fmt.Sprintf("etcd is degraded but has quorum: %s", etcdHealthy.Message)
	}

	conditions.Set(tcp, condition)
}

func (r *TalosControlPlaneReconciler) reconcileExternalReference(ctx context.Context, ref clusterv1.ContractVersionedObjectReference, cluster *clusterv1.Cluster) error {
	if !strings.HasSuffix(ref.Kind, clusterv1.TemplateSuffix) {
		return nil
	}

	obj, err := external.GetObjectFromContractVersionedRef(ctx, r.Client, ref, cluster.Namespace)
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

// reconcileEtcdMembers removes etcd members without a Machine and sets the EtcdClusterHealthy condition. It returns
// what the etcd health check observed on each Machine.
func (r *TalosControlPlaneReconciler) reconcileEtcdMembers(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (ctrl.Result, map[string]etcdMachineStatus, error) {
	var errs error
	// Audit the etcd member list to remove any nodes that no longer exist
	if err := r.auditEtcd(ctx, tcp, util.ObjectKey(cluster)); err != nil {
		errs = kerrors.NewAggregate([]error{errs, err})
	}

	health, err := r.etcdHealthcheck(ctx, tcp, machines.Items)
	if err != nil {
		// The reason tells the Available condition whether etcd still has quorum.
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.EtcdClusterHealthyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  health.conditionReason(),
			Message: fmt.Sprintf("Failed to perform etcd healthcheck: %v", err),
		})

		errs = kerrors.NewAggregate([]error{errs, err})
	} else {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.EtcdClusterHealthyCondition),
			Status:  metav1.ConditionTrue,
			Reason:  controlplanev1.EtcdClusterHealthyReason,
			Message: "etcd healthcheck successful",
		})
	}

	if errs != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, health.statuses, errs
	}

	return ctrl.Result{}, health.statuses, nil
}

func (r *TalosControlPlaneReconciler) reconcileNodeHealth(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if err := r.nodesHealthcheck(ctx, tcp, machines.Items); err != nil {
		reason := controlplanev1.ControlPlaneComponentsInspectionFailedReason

		if errors.Is(err, &errServiceUnhealthy{}) {
			reason = controlplanev1.ControlPlaneComponentsUnhealthyReason
		}

		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.ControlPlaneComponentsHealthyCondition),
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: fmt.Sprintf("Failed to perform control plane healthcheck: %v", err),
		})

		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	} else {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.ControlPlaneComponentsHealthyCondition),
			Status:  metav1.ConditionTrue,
			Reason:  controlplanev1.ControlPlaneComponentsHealthyReason,
			Message: "Control plane components are healthy",
		})
	}

	return ctrl.Result{}, nil
}

func (r *TalosControlPlaneReconciler) reconcileConditions(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (result ctrl.Result, err error) {
	if !conditions.Has(tcp, string(controlplanev1.MachinesBootstrapped)) {
		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesBootstrapped),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.WaitingForMachinesReason,
			Message: "Waiting for machines to bootstrap",
		})
	}

	return ctrl.Result{}, nil
}

// reconcileMachines rolls out, scales and bootstraps the control plane. controlPlane is the ControlPlane of the
// Machines, built if nil.
func (r *TalosControlPlaneReconciler) reconcileMachines(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList, controlPlane *ControlPlane) (res ctrl.Result, err error) {
	logger := r.Log.WithValues("namespace", tcp.Namespace, "talosControlPlane", tcp.Name)

	// If we've made it this far, we can assume that all ownedMachines are up to date
	numMachines := len(machines.Items)
	desiredReplicas := int(tcp.Spec.GetReplicas())

	if controlPlane == nil {
		controlPlane, err = newControlPlane(ctx, r.Client, cluster, tcp, collections.FromMachineList(machines))
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	needRollout := controlPlane.MachinesNeedingRollout()
	if len(needRollout) > 0 {
		logger.Info("rolling out control plane machines", "needRollout", needRollout.Names())
		apimeta.SetStatusCondition(&controlPlane.TCP.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesSpecUpToDateCondition),
			Status:  metav1.ConditionFalse,
			Reason:  controlplanev1.RollingUpdateInProgressReason,
			Message: fmt.Sprintf("Rolling %d replicas with outdated spec (%d replicas up to date)", len(needRollout), len(controlPlane.Machines)-len(needRollout)),
		})

		return r.upgradeControlPlane(ctx, cluster, tcp, controlPlane, needRollout)
	} else {
		if conditions.Has(controlPlane.TCP, string(controlplanev1.MachinesSpecUpToDateCondition)) {
			apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesSpecUpToDateCondition),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.MachinesSpecUpToDateReason,
				Message: "All control plane machines have up-to-date spec",
			})

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
			if !res.IsZero() {
				logger.Info("failed to scale down control plane", "error", err)

				return res, nil
			}
		}

		return res, err
	default:
		if !reflect.ValueOf(tcp.Spec.ControlPlaneConfig.InitConfig).IsZero() {
			tcp.Status.Bootstrapped = true

			apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesBootstrapped),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.MachinesBootstrappedReason,
				Message: "Control plane bootstrapped successfully",
			})
		}

		if !tcp.Status.Bootstrapped {
			if err := r.bootstrapCluster(ctx, tcp, machines.Items); err != nil {
				apimeta.SetStatusCondition(&controlPlane.TCP.Status.Conditions, metav1.Condition{
					Type:    string(controlplanev1.MachinesBootstrapped),
					Status:  metav1.ConditionFalse,
					Reason:  controlplanev1.WaitingForTalosBootReason,
					Message: fmt.Sprintf("Failed to bootstrap cluster: %v", err),
				})

				logger.Info("bootstrap failed, retrying in 20 seconds", "error", err)

				return ctrl.Result{RequeueAfter: time.Second * 20}, nil
			}

			apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
				Type:    string(controlplanev1.MachinesBootstrapped),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.MachinesBootstrappedReason,
				Message: "Control plane bootstrapped successfully",
			})

			tcp.Status.Bootstrapped = true
		}

		if conditions.Has(tcp, string(controlplanev1.MachinesAllReadyCondition)) {
			apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
				Type:    string(controlplanev1.ResizedCondition),
				Status:  metav1.ConditionTrue,
				Reason:  controlplanev1.ResizedReason,
				Message: "Control plane successfully resized",
			})
		}

		apimeta.SetStatusCondition(&tcp.Status.Conditions, metav1.Condition{
			Type:    string(controlplanev1.MachinesCreatedCondition),
			Status:  metav1.ConditionTrue,
			Reason:  controlplanev1.MachinesCreatedReason,
			Message: "Machines have been successfully created",
		})
	}

	return ctrl.Result{}, nil
}

// hasEmptyConditionReason reports whether a condition of the TalosControlPlane has an empty reason. The CRD schema
// (generated for []metav1.Condition) requires a reason of at least 1 character, so as long as a stored condition has
// none, e.g. a v1beta1 condition stored by an older controller version or a manual edit, the apiserver rejects every
// status patch of the object with "conditions[N].reason: Invalid value: \"\"".
func hasEmptyConditionReason(tcp *controlplanev1.TalosControlPlane) bool {
	return slices.ContainsFunc(tcp.Status.Conditions, func(c metav1.Condition) bool { return c.Reason == "" })
}

// sanitizeConditionReasons gives the conditions without a reason a placeholder one. It is only meant to fix up the
// stored conditions once, at the start of a reconcile: the conditions the controller sets always have a reason.
func sanitizeConditionReasons(tcp *controlplanev1.TalosControlPlane) {
	for i := range tcp.Status.Conditions {
		if tcp.Status.Conditions[i].Reason == "" {
			tcp.Status.Conditions[i].Reason = "NoReasonReported"
		}
	}
}

func patchTalosControlPlane(ctx context.Context, patchHelper *patch.Helper, tcp *controlplanev1.TalosControlPlane, opts ...patch.Option) error {
	// The stored conditions were fixed up at the start of the reconcile, so a condition without a reason was set
	// without one in this reconcile. The apiserver would reject the patch anyway: name the condition instead of
	// hiding the mistake behind a placeholder reason.
	for _, condition := range tcp.Status.Conditions {
		if condition.Reason == "" {
			return fmt.Errorf("condition %q of the TalosControlPlane was set without a reason", condition.Type)
		}
	}

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
			clusterv1.MachinesReadyCondition,
			clusterv1.MachinesUpToDateCondition,
			clusterv1.PausedCondition,
		}},
	)

	// Patch the object, ignoring conflicts on the conditions owned by this controller.
	return patchHelper.Patch(
		ctx,
		tcp,
		opts...,
	)
}
