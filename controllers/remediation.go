// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

const (
	// remediationInProgressAnnotation is set on the TalosControlPlane before an unhealthy Machine is removed from etcd
	// and deleted, until a replacement Machine is Ready or remediationInProgressTimeout expired. Its value is a JSON
	// encoded remediationData. While it is set, no other Machine is remediated and the control plane is not scaled down.
	// Earlier versions of the controller set it to the bare name of the remediated Machine; such a value is converted
	// by reconcileRemediationInProgress.
	remediationInProgressAnnotation = "controlplane.cluster.x-k8s.io/remediation-in-progress"

	// remediationForAnnotation is set on the Machine that replaced a remediated Machine. Its value is the
	// remediationData of that remediation, which is used to count retries if the replacement is unhealthy too.
	remediationForAnnotation = "controlplane.cluster.x-k8s.io/remediation-for"
)

// Remediation limits. KubeadmControlPlane makes them configurable; TalosControlPlane has no API fields for them yet.
const (
	// remediationInProgressTimeout is how long a remediation blocks the next one when no replacement becomes Ready.
	remediationInProgressTimeout = 30 * time.Minute

	// remediationStartTimeout is how long after a remediation was recorded the remediated Machine may still be seen
	// neither leaving etcd nor being deleted, before the remediation is considered not started. That happens when the
	// controller stopped between recording the remediation and removing the Machine from etcd; it is also seen for
	// a short while when the cached Machines lag behind.
	remediationStartTimeout = time.Minute

	// remediationMinHealthyPeriod is how long a replacement must stay healthy for its own remediation to start a new
	// retry sequence instead of counting as a retry of the previous remediation.
	remediationMinHealthyPeriod = time.Hour

	// remediationRetryPeriod is the minimum time between two remediations of the same retry sequence.
	remediationRetryPeriod = 5 * time.Minute

	// remediationMaxRetry is the maximum number of retries of a remediation sequence.
	remediationMaxRetry = 3
)

// Reasons of the OwnerRemediated condition of control plane Machines; they have the same values as for
// KubeadmControlPlane and MachineSet Machines.
const (
	remediationDeferredReason        = clusterv1.MachineSetMachineRemediationDeferredReason
	cannotBeRemediatedReason         = clusterv1.MachineSetMachineCannotBeRemediatedReason
	remediationMachineDeletingReason = clusterv1.MachineSetMachineRemediationMachineDeletingReason
	remediationInternalErrorReason   = clusterv1.InternalErrorReason
)

// remediationData is stored in remediationInProgressAnnotation and remediationForAnnotation.
type remediationData struct {
	// Machine is the name of the remediated Machine.
	Machine string `json:"machine"`

	// Timestamp is when the remediation happened.
	Timestamp metav1.Time `json:"timestamp"`

	// RetryCount is the number of retries of the remediation sequence: a retry happens when a Machine that
	// replaced a remediated Machine is remediated too, within remediationMinHealthyPeriod.
	RetryCount int `json:"retryCount"`
}

func remediationDataFromAnnotation(value string) (*remediationData, error) {
	data := &remediationData{}
	if err := json.Unmarshal([]byte(value), data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal remediation data %q: %w", value, err)
	}

	return data, nil
}

func (d *remediationData) marshal() (string, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("failed to marshal remediation data: %w", err)
	}

	return string(b), nil
}

// remediationInProgress reports whether a remediation is waiting for its replacement Machine.
func remediationInProgress(tcp *controlplanev1.TalosControlPlane) bool {
	_, ok := tcp.Annotations[remediationInProgressAnnotation]

	return ok
}

// reconcileRemediationAndMachines runs the two steps that delete control plane Machines: remediation, then scaling
// and rollout. When remediation deleted a Machine, or tried to, scaling and rollout are skipped in this reconcile, so that a single
// reconcile never deletes two Machines and the next one starts from a fresh list of Machines.
func (r *TalosControlPlaneReconciler) reconcileRemediationAndMachines(ctx context.Context, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) (ctrl.Result, error) {
	// A Machine that left etcd but was not deleted must be dealt with first: remediation would wait for it forever.
	if acted, err := r.reconcileEtcdLeavingMachines(ctx, tcp, machines.Items); acted || err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	result, remediating, err := r.reconcileUnhealthyMachines(ctx, tcp, cluster, machines.Items)
	if remediating {
		return result, err
	}

	machinesResult, machinesErr := r.reconcileMachines(ctx, cluster, tcp, machines)

	return util.LowestNonZeroResult(result, machinesResult), kerrors.NewAggregate([]error{err, machinesErr})
}

// reconcileEtcdLeavingMachines completes the removal of control plane Machines that are marked as leaving etcd but
// are not being deleted. This happens when deleting the Machine failed after its member left etcd, or when the
// controller stopped in between. Such a Machine is skipped by the etcd health check and defers every remediation, so
// it can't be left alone: if its etcd member is gone, the Machine is deleted; if it is still an etcd member, the mark
// is removed, so that the Machine counts again and is removed through remediation or scale down if needed.
//
// It handles at most one Machine per reconcile, and reports whether it acted on one: no other Machine may be deleted
// in the same reconcile then.
func (r *TalosControlPlaneReconciler) reconcileEtcdLeavingMachines(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machineList []clusterv1.Machine) (bool, error) {
	machines := make([]*clusterv1.Machine, 0, len(machineList))
	for i := range machineList {
		machines = append(machines, &machineList[i])
	}

	for _, m := range machines {
		if !m.DeletionTimestamp.IsZero() || m.Annotations[etcdLeavingAnnotation] != "true" {
			continue
		}

		members, ok, err := r.etcdMembersVia(ctx, tcp, m, machines)
		if err != nil {
			return true, fmt.Errorf("failed to check whether machine %s left etcd: %w", m.Name, err)
		}

		if !ok {
			r.Log.Info("no other control plane machine to check whether the machine left etcd", "machine", m.Name)

			return false, nil
		}

		if findEtcdMember(members, m) != nil {
			r.Log.Info("machine marked as leaving etcd is still an etcd member, removing the mark", "machine", m.Name)

			return true, r.setEtcdLeavingAnnotation(ctx, m, false)
		}

		r.Log.Info("deleting control plane machine that left etcd", "machine", m.Name)

		if err := r.Client.Delete(ctx, m); err != nil && !apierrors.IsNotFound(err) {
			return true, fmt.Errorf("failed to delete machine %s that left etcd: %w", m.Name, err)
		}

		return true, nil
	}

	return false, nil
}

// reconcileUnhealthyMachines remediates control plane Machines that a MachineHealthCheck marked as unhealthy
// (HealthCheckSucceeded=False) and left to their owner to remediate (OwnerRemediated=False), by removing the
// Machine from etcd and deleting it. The regular scale up then creates a replacement. It is modelled on the
// KubeadmControlPlane remediation.
//
// A Machine is only remediated when:
//   - the control plane is initialized and has more than one Machine,
//   - no other remediation is in progress and the retry limits allow it,
//   - no other control plane Machine is still provisioning, being deleted or leaving etcd,
//   - removing its etcd member keeps etcd quorum, given the current etcd members and their health.
//
// Otherwise the reason is surfaced in the OwnerRemediated condition of the Machine. At most one Machine is deleted
// per reconcile; remediating reports whether remediation acted on a Machine in this reconcile, by removing
// it from etcd or deleting it, even if that failed: no other Machine may be deleted in the same reconcile then.
func (r *TalosControlPlaneReconciler) reconcileUnhealthyMachines(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	cluster *clusterv1.Cluster,
	machineList []clusterv1.Machine,
) (_ ctrl.Result, remediating bool, retErr error) {
	log := r.Log.WithValues(
		"TalosControlPlane", fmt.Sprintf("%s/%s", tcp.Namespace, tcp.Name),
		"Cluster", cluster.Name,
	)
	now := time.Now().UTC()

	machines := make([]*clusterv1.Machine, 0, len(machineList))
	for i := range machineList {
		machines = append(machines, &machineList[i])
	}

	// This runs first, so that an annotation set by an earlier version is converted before the Machines created in
	// this reconcile copy it, even if a later step fails.
	if err := r.reconcileRemediationInProgress(ctx, tcp, machines, now, log); err != nil {
		return ctrl.Result{}, false, err
	}

	// The MachineHealthCheck leaves OwnerRemediated to the owner once set: remove it from Machines that are healthy
	// again, so that a Machine that recovered while its remediation was deferred is not deleted later.
	if err := r.cleanupOwnerRemediated(ctx, machines); err != nil {
		return ctrl.Result{}, false, err
	}

	target := machineToBeRemediated(machines)
	if target == nil {
		return ctrl.Result{}, false, nil
	}

	if !target.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, false, nil
	}

	log = log.WithValues("machine", target.Name)

	patchHelper, err := patch.NewHelper(target, r.Client)
	if err != nil {
		return ctrl.Result{}, false, err
	}

	defer func() {
		if err := patchHelper.Patch(ctx, target, patch.WithOwnedConditions{Conditions: []string{
			clusterv1.MachineOwnerRemediatedCondition,
		}}); err != nil && !apierrors.IsNotFound(err) {
			retErr = kerrors.NewAggregate([]error{retErr, fmt.Errorf("failed to patch control plane machine %s: %w", target.Name, err)})
		}
	}()

	setOwnerRemediated := func(reason, message string) {
		log.Info("a control plane machine needs remediation: "+message, "reason", reason)

		conditions.Set(target, metav1.Condition{
			Type:    clusterv1.MachineOwnerRemediatedCondition,
			Status:  metav1.ConditionFalse,
			Reason:  reason,
			Message: message,
		})
	}

	if remediationInProgress(tcp) {
		setOwnerRemediated(remediationDeferredReason, "TalosControlPlane waiting for the previous remediation to complete before triggering remediation")

		return ctrl.Result{}, false, nil
	}

	data, reason, message := checkRemediationRetryLimits(target, now, log)
	if reason != "" {
		setOwnerRemediated(reason, message)

		return ctrl.Result{}, false, nil
	}

	if !tcp.Status.Initialized && !ptr.Deref(tcp.Status.Initialization.ControlPlaneInitialized, false) {
		setOwnerRemediated(remediationDeferredReason, "TalosControlPlane can't remediate before the control plane is initialized")

		return ctrl.Result{}, false, nil
	}

	if len(machines) <= 1 {
		setOwnerRemediated(cannotBeRemediatedReason, "TalosControlPlane can't remediate if current replicas are less or equal to 1")

		return ctrl.Result{}, false, nil
	}

	for _, m := range machines {
		if m.Name == target.Name {
			continue
		}

		if !m.DeletionTimestamp.IsZero() || m.Annotations[etcdLeavingAnnotation] == "true" {
			setOwnerRemediated(remediationDeferredReason, fmt.Sprintf("TalosControlPlane waiting for the deletion of control plane machine %s to complete before triggering remediation", m.Name))

			return ctrl.Result{}, false, nil
		}

		if !collections.IsUnhealthy(m) && !m.Status.NodeRef.IsDefined() {
			setOwnerRemediated(remediationDeferredReason, fmt.Sprintf("TalosControlPlane waiting for the provisioning of control plane machine %s to complete before triggering remediation", m.Name))

			return ctrl.Result{}, false, nil
		}
	}

	safe, details, err := r.canSafelyRemediateMachine(ctx, tcp, target, machines)
	if err != nil {
		log.Info("failed to check etcd cluster health before remediation", "error", err)
		setOwnerRemediated(remediationDeferredReason, "Failed to check etcd cluster health before remediation")

		return ctrl.Result{}, false, nil
	}

	if !safe {
		setOwnerRemediated(cannotBeRemediatedReason, "TalosControlPlane can't remediate this machine because this could result in etcd quorum loss: "+details)

		return ctrl.Result{}, false, nil
	}

	value, err := data.marshal()
	if err != nil {
		return ctrl.Result{}, false, err
	}

	// Record the remediation before the Machine is touched, rather than in the deferred patch of the
	// TalosControlPlane: if the controller stopped, or the deferred patch failed, after the Machine was deleted, the
	// remediation would be lost, and with it the retry count and what keeps other Machines from being deleted before
	// the replacement is up.
	if err := r.patchRemediationInProgress(ctx, tcp, value); err != nil {
		setOwnerRemediated(remediationInternalErrorReason, "Failed to record the remediation, please check controller logs for errors")

		return ctrl.Result{}, true, err
	}

	// Remove the member from etcd before deleting the Machine, as the scale down does: a graceful leave also
	// forfeits etcd leadership, and an unreachable Machine is removed through another control plane Machine.
	if err := r.removeMachineFromEtcd(ctx, tcp, target, machines); err != nil {
		setOwnerRemediated(remediationInternalErrorReason, "Failed to remove the machine from etcd, please check controller logs for errors")

		// The Machine is kept as an etcd member, so the remediation did not happen.
		if clearErr := r.patchRemediationInProgress(ctx, tcp, ""); clearErr != nil {
			err = kerrors.NewAggregate([]error{err, clearErr})
		}

		return ctrl.Result{}, true, err
	}

	// If the deletion fails, the remediation stays recorded: the Machine left etcd, so it is deleted by
	// reconcileEtcdLeavingMachines.
	if err := r.Client.Delete(ctx, target); err != nil && !apierrors.IsNotFound(err) {
		setOwnerRemediated(remediationInternalErrorReason, "Failed to delete the machine, please check controller logs for errors")

		return ctrl.Result{}, true, fmt.Errorf("failed to delete unhealthy machine %s: %w", target.Name, err)
	}

	log.Info("deleted unhealthy control plane machine, a replacement will be created", "retryCount", data.RetryCount)

	conditions.Set(target, metav1.Condition{
		Type:    clusterv1.MachineOwnerRemediatedCondition,
		Status:  metav1.ConditionFalse,
		Reason:  remediationMachineDeletingReason,
		Message: "Machine is deleting",
	})

	return ctrl.Result{RequeueAfter: 10 * time.Second}, true, nil
}

// patchRemediationInProgress sets the remediation-in-progress annotation of the TalosControlPlane to value, or
// removes it if value is empty, and patches it right away. Only the annotation is patched; the other changes made
// to the TalosControlPlane in this reconcile are left to the deferred patch.
func (r *TalosControlPlaneReconciler) patchRemediationInProgress(ctx context.Context, tcp *controlplanev1.TalosControlPlane, value string) error {
	before := tcp.DeepCopy()
	after := tcp.DeepCopy()

	if value != "" {
		annotations.AddAnnotations(after, map[string]string{remediationInProgressAnnotation: value})
	} else {
		delete(after.Annotations, remediationInProgressAnnotation)
	}

	if err := r.Client.Patch(ctx, after, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("failed to patch the remediation-in-progress annotation: %w", err)
	}

	tcp.Annotations = after.Annotations

	return nil
}

// cleanupOwnerRemediated removes the OwnerRemediated condition from Machines whose MachineHealthCheck reports
// them as healthy again.
func (r *TalosControlPlaneReconciler) cleanupOwnerRemediated(ctx context.Context, machines []*clusterv1.Machine) error {
	var errs []error

	for _, m := range machines {
		if !m.DeletionTimestamp.IsZero() ||
			!conditions.IsTrue(m, clusterv1.MachineHealthCheckSucceededCondition) ||
			!conditions.IsFalse(m, clusterv1.MachineOwnerRemediatedCondition) {
			continue
		}

		patchHelper, err := patch.NewHelper(m, r.Client)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		conditions.Delete(m, clusterv1.MachineOwnerRemediatedCondition)

		if err := patchHelper.Patch(ctx, m, patch.WithOwnedConditions{Conditions: []string{
			clusterv1.MachineOwnerRemediatedCondition,
		}}); err != nil {
			errs = append(errs, err)
		}
	}

	return kerrors.NewAggregate(errs)
}

// reconcileRemediationInProgress removes the remediation-in-progress annotation once the remediation completed:
// a Machine created after the remediation is Ready, or the remediated Machine is gone and enough Machines are Ready,
// or remediationInProgressTimeout expired after the remediated Machine is gone or replaced, so that a replacement that
// never becomes Ready can't block remediation forever. The remediation data moves to the replacement Machine, where it
// is used to count retries; Machines created while the remediation is in progress already got it at creation.
//
// The annotation is also removed if the remediation never started: the remediated Machine is still neither leaving
// etcd nor being deleted remediationStartTimeout after the remediation was recorded.
func (r *TalosControlPlaneReconciler) reconcileRemediationInProgress(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	machines []*clusterv1.Machine,
	now time.Time,
	log logr.Logger,
) error {
	value, ok := tcp.Annotations[remediationInProgressAnnotation]
	if !ok {
		return nil
	}

	data, err := remediationDataFromAnnotation(value)
	if err != nil {
		data = legacyRemediationData(value, machines, now)
		if data == nil {
			log.Info("removing invalid remediation-in-progress annotation", "error", err)
			delete(tcp.Annotations, remediationInProgressAnnotation)

			return nil
		}

		if value, err = data.marshal(); err != nil {
			return err
		}

		// The deferred patch of the TalosControlPlane persists the converted annotation, so that its timestamp
		// stays the same in the next reconciles.
		log.Info("converting remediation-in-progress annotation set by an earlier version", "remediatedMachine", data.Machine, "timestamp", data.Timestamp)
		annotations.AddAnnotations(tcp, map[string]string{remediationInProgressAnnotation: value})
	}

	var (
		replacement      *clusterv1.Machine
		remediated       *clusterv1.Machine
		remediatedExists bool
		readyMachines    int32
	)

	for _, m := range machines {
		if m.Name == data.Machine {
			remediated = m
			remediatedExists = true

			continue
		}

		if !m.DeletionTimestamp.IsZero() {
			continue
		}

		if conditions.IsTrue(m, clusterv1.ReadyCondition) {
			readyMachines++
		}

		// The oldest Machine created after the remediation is its replacement.
		if m.CreationTimestamp.After(data.Timestamp.Time) &&
			(replacement == nil || m.CreationTimestamp.Before(&replacement.CreationTimestamp)) {
			replacement = m
		}
	}

	// The remediation is recorded before the Machine is removed from etcd and deleted. A remediated Machine that is
	// neither leaving etcd nor being deleted means that the controller stopped in between: the Machine is still an
	// etcd member, so the remediation did not happen and has nothing to hand over to a replacement.
	if remediated != nil &&
		remediated.DeletionTimestamp.IsZero() &&
		remediated.Annotations[etcdLeavingAnnotation] != "true" &&
		now.After(data.Timestamp.Add(remediationStartTimeout)) {
		log.Info("removing the remediation-in-progress annotation, the remediated machine was neither removed from etcd nor deleted", "remediatedMachine", data.Machine)
		delete(tcp.Annotations, remediationInProgressAnnotation)

		return nil
	}

	switch {
	case replacement != nil && conditions.IsTrue(replacement, clusterv1.ReadyCondition):
		log.Info("remediation completed, the replacement machine is ready", "remediatedMachine", data.Machine, "replacement", replacement.Name)
	case !remediatedExists && readyMachines >= tcp.Spec.GetReplicas():
		log.Info("remediation completed, the control plane has enough ready machines", "remediatedMachine", data.Machine, "readyMachines", readyMachines)
	// Without a replacement, the remediation data could only be recorded on the replacement if the annotation is
	// kept, so it is kept while the remediated Machine still exists; other remediations wait for its deletion anyway.
	case now.After(data.Timestamp.Add(remediationInProgressTimeout)) && (replacement != nil || !remediatedExists):
		log.Info("remediation timed out waiting for a ready replacement machine", "remediatedMachine", data.Machine, "timeout", remediationInProgressTimeout)
	default:
		return nil
	}

	if replacement != nil && replacement.Annotations[remediationForAnnotation] == "" {
		patchHelper, err := patch.NewHelper(replacement, r.Client)
		if err != nil {
			return err
		}

		annotations.AddAnnotations(replacement, map[string]string{remediationForAnnotation: value})

		if err := patchHelper.Patch(ctx, replacement); err != nil {
			return fmt.Errorf("failed to record the remediation on replacement machine %s: %w", replacement.Name, err)
		}
	}

	delete(tcp.Annotations, remediationInProgressAnnotation)

	return nil
}

// legacyRemediationData converts a remediation-in-progress annotation value set by earlier versions of the controller,
// the bare name of the remediated Machine, to remediationData. It returns nil if value is not a Machine name.
//
// Those versions set the annotation right before deleting the Machine, so the deletion timestamp of the Machine tells
// when the remediation happened. If the Machine is gone, or was not deleted, now is used instead: the remediation then
// completes once enough Machines are Ready, as with those versions, or times out later rather than sooner.
func legacyRemediationData(value string, machines []*clusterv1.Machine, now time.Time) *remediationData {
	if len(validation.IsDNS1123Subdomain(value)) > 0 {
		return nil
	}

	data := &remediationData{
		Machine:   value,
		Timestamp: metav1.Time{Time: now},
	}

	for _, m := range machines {
		if m.Name == value && !m.DeletionTimestamp.IsZero() {
			data.Timestamp = *m.DeletionTimestamp
		}
	}

	return data
}

// machineToBeRemediated returns the Machine to remediate first among the Machines that a MachineHealthCheck
// marked for remediation by their owner: a Machine with the remediate-machine annotation, then a Machine
// that failed to provision (no nodeRef), then the oldest Machine.
func machineToBeRemediated(machines []*clusterv1.Machine) *clusterv1.Machine {
	var candidates []*clusterv1.Machine

	for _, m := range machines {
		if collections.IsUnhealthyAndOwnerRemediated(m) {
			candidates = append(candidates, m)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	slices.SortStableFunc(candidates, func(a, b *clusterv1.Machine) int {
		if annotations.HasRemediateMachine(a) != annotations.HasRemediateMachine(b) {
			if annotations.HasRemediateMachine(a) {
				return -1
			}

			return 1
		}

		if a.Status.NodeRef.IsDefined() != b.Status.NodeRef.IsDefined() {
			if !a.Status.NodeRef.IsDefined() {
				return -1
			}

			return 1
		}

		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			if a.CreationTimestamp.Before(&b.CreationTimestamp) {
				return -1
			}

			return 1
		}

		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	return candidates[0]
}

// checkRemediationRetryLimits returns the remediation data to record for the target, or the reason and message
// why the retry limits don't allow to remediate it now.
//
// A remediation of a Machine that replaced a Machine remediated less than remediationMinHealthyPeriod ago is a retry
// of that remediation: it waits for remediationRetryPeriod after the previous one, and stops after remediationMaxRetry
// retries, so that a Machine that always fails, e.g. because of a quota issue, isn't replaced forever.
func checkRemediationRetryLimits(target *clusterv1.Machine, now time.Time, log logr.Logger) (data *remediationData, reason, message string) {
	data = &remediationData{
		Machine:   target.Name,
		Timestamp: metav1.Time{Time: now},
	}

	value, ok := target.Annotations[remediationForAnnotation]
	if !ok {
		return data, "", ""
	}

	last, err := remediationDataFromAnnotation(value)
	if err != nil {
		log.Info("ignoring invalid remediation-for annotation", "error", err)

		return data, "", ""
	}

	// A replacement that stayed healthy for remediationMinHealthyPeriod starts a new retry sequence.
	if !last.Timestamp.Add(remediationMinHealthyPeriod).After(now) {
		return data, "", ""
	}

	data.RetryCount = last.RetryCount

	if last.Timestamp.Add(remediationRetryPeriod).After(now) {
		return data, remediationDeferredReason, fmt.Sprintf("TalosControlPlane can't remediate this machine because the operation already failed in the latest %s", remediationRetryPeriod)
	}

	if data.RetryCount >= remediationMaxRetry {
		return data, cannotBeRemediatedReason, fmt.Sprintf("TalosControlPlane can't remediate this machine because the operation already failed %d times", remediationMaxRetry)
	}

	data.RetryCount++

	return data, "", ""
}

// canSafelyRemediateMachine checks whether the etcd member of the target can be removed without losing etcd quorum.
// It returns an error if the etcd cluster could not be inspected.
func (r *TalosControlPlaneReconciler) canSafelyRemediateMachine(
	ctx context.Context,
	tcp *controlplanev1.TalosControlPlane,
	target *clusterv1.Machine,
	machines []*clusterv1.Machine,
) (bool, string, error) {
	etcd := r.etcdMembers()

	healthy := make(map[string]bool, len(machines))

	var (
		members []*machineapi.EtcdMember
		listErr error
	)

	for _, m := range machines {
		if !m.Status.NodeRef.IsDefined() {
			continue
		}

		if err := etcd.ServiceHealth(ctx, tcp, m); err != nil {
			r.Log.Info("etcd is not healthy on control plane machine", "machine", m.Name, "error", err)

			continue
		}

		healthy[m.Name] = true

		// List the members through a healthy machine that stays in the cluster.
		if members == nil && m.Name != target.Name {
			members, listErr = etcd.MemberList(ctx, tcp, m)
		}
	}

	// The target is the only healthy etcd member left to ask.
	if members == nil && healthy[target.Name] {
		members, listErr = etcd.MemberList(ctx, tcp, target)
	}

	if len(members) == 0 {
		if listErr == nil {
			listErr = fmt.Errorf("no healthy etcd member to list the etcd members from")
		}

		return false, "", listErr
	}

	safe, details := canSafelyRemoveEtcdMember(members, target, machines, healthy)

	r.Log.Info("checked etcd quorum before remediation", "machine", target.Name, "members", len(members), "safe", safe, "details", details)

	return safe, details, nil
}

// canSafelyRemoveEtcdMember reports whether the etcd member of the target can be removed without losing etcd quorum,
// like KubeadmControlPlane's targetEtcdClusterHealthy. healthy tells which machines run a healthy etcd service.
//
// Removing a member is itself a change that needs a quorum of the current voting members to commit, and the remaining
// voting members then need a quorum of their own: in a 3 member cluster, the 2 other members must be healthy, in a 5
// member cluster, 3 of the 4 others. Learners block remediation until they are promoted. Members that can't be matched
// to a machine count as unhealthy; when the target's member can't be identified, it is assumed to be one of them, so
// anything unknown makes the check stricter.
func canSafelyRemoveEtcdMember(members []*machineapi.EtcdMember, target *clusterv1.Machine, machines []*clusterv1.Machine, healthy map[string]bool) (bool, string) {
	var (
		learners, remainingVoting, remainingHealthy int
		targetMember                                *machineapi.EtcdMember
	)

	for _, member := range members {
		if member.IsLearner {
			learners++

			continue
		}

		if targetMember == nil && etcdMemberMatchesMachine(member, target) {
			targetMember = member

			continue
		}

		remainingVoting++

		for _, m := range machines {
			if m.Name != target.Name && healthy[m.Name] && etcdMemberMatchesMachine(member, m) {
				remainingHealthy++

				break
			}
		}
	}

	if learners > 0 {
		return false, fmt.Sprintf("%d etcd members are learners that did not join the cluster yet", learners)
	}

	currentVoting, currentHealthy := remainingVoting, remainingHealthy
	if targetMember != nil {
		currentVoting++

		if healthy[target.Name] {
			currentHealthy++
		}
	}

	if quorum := currentVoting/2 + 1; currentHealthy < quorum {
		return false, fmt.Sprintf("%d of %d etcd members are healthy, removing a member needs %d", currentHealthy, currentVoting, quorum)
	}

	if quorum := remainingVoting/2 + 1; remainingHealthy < quorum {
		return false, fmt.Sprintf("%d of the %d remaining etcd members would be healthy, quorum needs %d", remainingHealthy, remainingVoting, quorum)
	}

	return true, fmt.Sprintf("%d of the %d remaining etcd members are healthy", remainingHealthy, remainingVoting)
}
