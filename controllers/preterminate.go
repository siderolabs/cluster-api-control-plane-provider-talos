// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

const (
	// PreTerminateHookCleanupAnnotation is the Cluster API pre-terminate lifecycle hook this
	// provider puts on every control plane Machine it owns. While the annotation is present the
	// core Machine controller holds the Machine right before termination — after drain and
	// volume detach, before the infrastructure provider is allowed to delete the
	// InfraMachine — which is the only point in the deletion pipeline where the etcd member
	// can still be removed cleanly and the node has not been powered off yet.
	//
	// The name mirrors KCP's `<prefix>/kcp-cleanup` (see
	// sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2.PreTerminateHookCleanupAnnotation).
	PreTerminateHookCleanupAnnotation = clusterv1.PreTerminateDeleteHookAnnotationPrefix + "/tcp-cleanup"

	// etcdCleanupObservedAtAnnotation records, in RFC3339, when this controller first saw the
	// Machine waiting at the pre-terminate phase. It anchors the fail-open deadline to the
	// Machine itself so a controller restart does not reset the clock.
	etcdCleanupObservedAtAnnotation = "controlplane.cluster.x-k8s.io/etcd-cleanup-observed-at"

	// defaultEtcdCleanupTimeout is how long etcd cleanup is retried before the hook is released
	// anyway, leaving the orphaned member for auditEtcd to collect.
	defaultEtcdCleanupTimeout = 2 * time.Minute

	// preTerminateRequeueAfter is the retry interval for the pre-terminate handler while it is
	// waiting for something outside its control (an earlier deletion phase, another Machine's
	// membership change).
	preTerminateRequeueAfter = 15 * time.Second

	// etcdCallTimeout bounds a single round of Talos API calls, matching the rest of the
	// package.
	etcdCallTimeout = 5 * time.Second
)

// Machine event reasons emitted by the pre-terminate etcd cleanup.
const (
	etcdMemberLeftEvent           = "EtcdMemberLeft"
	etcdMemberRemovedViaPeerEvent = "EtcdMemberRemovedViaPeer"
	etcdCleanupSkippedEvent       = "EtcdCleanupSkipped"
	etcdCleanupOrphanedEvent      = "EtcdCleanupOrphaned"
)

// etcdCleanupTimeout is the fail-open deadline for the pre-terminate handler.
func (r *TalosControlPlaneReconciler) etcdCleanupTimeout() time.Duration {
	if r.EtcdCleanupTimeout > 0 {
		return r.EtcdCleanupTimeout
	}

	return defaultEtcdCleanupTimeout
}

// desiredMachineAnnotations returns the annotations a control plane Machine owned by this
// TalosControlPlane should carry at creation time.
func (r *TalosControlPlaneReconciler) desiredMachineAnnotations(tcp *controlplanev1.TalosControlPlane) map[string]string {
	annotations := copyStringMap(tcp.Spec.MachineTemplate.ObjectMeta.Annotations)

	if r.EnableMachinePreTerminateHook {
		if annotations == nil {
			annotations = map[string]string{}
		}

		annotations[PreTerminateHookCleanupAnnotation] = ""
	}

	return annotations
}

// reconcileMachinePreTerminateHooks stamps the pre-terminate hook on the control plane Machines
// this TalosControlPlane owns and services the hook on the ones being deleted.
//
// It must run early in the reconcile: a Machine parked at the pre-terminate phase blocks its
// InfraMachine from being deleted, and every gate below it (control plane endpoint, node health,
// etcd health) can be failing exactly when a control plane Machine is on its way out.
func (r *TalosControlPlaneReconciler) reconcileMachinePreTerminateHooks(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	machines *clusterv1.MachineList,
) (ctrl.Result, error) {
	owned := ownedControlPlaneMachines(tcp, machines)

	// Adopting machines is best-effort on this pass: a stamp patch that keeps failing must not
	// park a deletion that is already in flight behind it, so its error is carried alongside the
	// servicing below instead of returned ahead of it.
	stampErr := r.stampPreTerminateHooks(ctx, owned)

	result, err := r.servicePreTerminateHooks(ctx, cluster, tcp, owned)

	return result, kerrors.NewAggregate([]error{stampErr, err})
}

// servicePreTerminateHooks picks the one deleting Machine to act on this pass and acts on it.
func (r *TalosControlPlaneReconciler) servicePreTerminateHooks(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	owned []*clusterv1.Machine,
) (ctrl.Result, error) {
	deleting, unhooked := deletingMachines(owned)

	// A deleting Machine without our hook is already past the point where its etcd membership
	// could be resolved, so whether its member is still in the cluster is unknown. Removing a
	// second member while that one is unaccounted for is how a three-member cluster ends up with
	// one live member out of two and loses quorum. Wait for it to go away first.
	// Mirrors KCP (controlplane/kubeadm/internal/controllers/controller.go:1344-1348).
	if unhooked != nil {
		r.Log.Info("waiting for a deleting control plane machine without the pre-terminate hook to go away",
			"machine", unhooked.Name)

		return ctrl.Result{RequeueAfter: preTerminateRequeueAfter}, nil
	}

	if len(deleting) == 0 {
		return ctrl.Result{}, nil
	}

	// Serialization: etcd tolerates exactly one membership change at a time, so only the
	// Machine with the oldest deletionTimestamp is serviced on this pass. The rest are picked
	// up on a later one, which keeps the handler deterministic and re-entrant.
	//
	// Note that the fail-open deadline is therefore a per-serviced-machine promise, not a
	// per-machine one: an oldest Machine stuck earlier in its deletion (draining, say) holds up
	// younger hooked Machines without their clocks having started. That matches KCP, and Cluster
	// API's own nodeDrainTimeoutSeconds is what bounds the case that causes it.
	result, err := r.reconcilePreTerminateHookForMachine(ctx, cluster, tcp, deleting[0], owned)
	if err != nil {
		return result, err
	}

	if len(deleting) > 1 && result.RequeueAfter == 0 {
		result.RequeueAfter = preTerminateRequeueAfter
	}

	return result, nil
}

// reconcilePreTerminateHookForMachine resolves etcd membership for a single deleting Machine and
// releases the hook when it is done (or when it has given up).
func (r *TalosControlPlaneReconciler) reconcilePreTerminateHookForMachine(
	ctx context.Context,
	cluster *clusterv1.Cluster,
	tcp *controlplanev1.TalosControlPlane,
	victim *clusterv1.Machine,
	owned []*clusterv1.Machine,
) (ctrl.Result, error) {
	// Phase gate: only act once the core Machine controller reports that it is blocked on
	// pre-terminate hooks, i.e. after drain and volume detach have finished. Removing the etcd
	// member any earlier takes the API server down while the node is still being drained.
	//
	// Deliberate deviation from KCP: we do NOT wait for other pre-terminate hooks to finish
	// first. KCP defers to user hooks so that kubelet keeps working while they run. The hooks
	// this provider coexists with are reset-style — they wipe and halt the node — and must run
	// AFTER etcd membership is resolved. Two hooks that both insist on running last deadlock,
	// so this one deliberately acts first and reset hooks run once it has released.
	deletingCondition := conditions.Get(victim, clusterv1.MachineDeletingCondition)
	if deletingCondition == nil ||
		deletingCondition.Status != metav1.ConditionTrue ||
		deletingCondition.Reason != clusterv1.MachineDeletingWaitingForPreTerminateHookReason {
		r.Log.Info("waiting for the machine deletion workflow to reach the pre-terminate phase", "machine", victim.Name)

		return ctrl.Result{RequeueAfter: preTerminateRequeueAfter}, nil
	}

	// Anchor the fail-open deadline before the first etcd call so a crash mid-cleanup cannot
	// restart the clock.
	if err := r.stampEtcdCleanupObservedAt(ctx, victim); err != nil {
		return ctrl.Result{}, err
	}

	// Whole-cluster or whole-control-plane teardown: every member is going away, so removing
	// them one at a time is both wrong (the last member cannot forfeit leadership to anyone)
	// and slow. Mirrors KCP's reconcileDelete.
	//
	// The Cluster is never nil here: Reconcile returns before reaching this code when the owner
	// Cluster is missing or has not set its OwnerRef yet.
	switch {
	case !cluster.DeletionTimestamp.IsZero():
		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdCleanupSkippedEvent,
			"cluster is being deleted, skipping etcd member removal")
	case !tcp.DeletionTimestamp.IsZero():
		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdCleanupSkippedEvent,
			"control plane is being deleted, skipping etcd member removal")
	}

	// Removing the last control plane machine leaves no peer to verify against or to remove the
	// member through, and etcd is about to disappear with it. Same check as KCP's
	// `controlPlane.Machines.Len() <= 1`, counting deleting machines.
	if len(owned) <= 1 {
		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdCleanupSkippedEvent,
			"last control plane machine, skipping etcd member removal")
	}

	peer := selectEtcdPeer(owned, victim)
	if peer == nil {
		return r.failOpenOrRetry(ctx, victim,
			errors.Errorf("no healthy control plane machine is available to verify etcd membership"))
	}

	peerClient, err := r.etcdClientFor(ctx, tcp, *peer)
	if err != nil {
		return r.failOpenOrRetry(ctx, victim, errors.Wrapf(err, "failed to connect to control plane machine %q", peer.Name))
	}

	defer peerClient.Close() //nolint:errcheck

	member, err := findEtcdMember(ctx, peerClient, victim)
	if err != nil {
		return r.failOpenOrRetry(ctx, victim, errors.Wrapf(err, "failed to list etcd members via machine %q", peer.Name))
	}

	if member == nil {
		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdCleanupSkippedEvent,
			"machine is no longer an etcd member, skipping etcd member removal")
	}

	// Mark the machine as leaving etcd so the health check stops counting it even if this
	// reconcile dies between here and the member actually leaving.
	if err := r.markEtcdLeaving(ctx, victim); err != nil {
		return ctrl.Result{}, err
	}

	leaveErr := r.etcdLeaveFromVictim(ctx, tcp, victim)
	if leaveErr == nil {
		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdMemberLeftEvent,
			fmt.Sprintf("etcd member %q left the cluster", member.Hostname))
	}

	r.Log.Info("graceful etcd leave failed, removing the member through a peer",
		"machine", victim.Name, "peer", peer.Name, "error", leaveErr)

	if removeErr := r.forceEtcdLeave(ctx, peerClient, member); removeErr != nil {
		return r.failOpenOrRetry(ctx, victim, kerrors.NewAggregate([]error{leaveErr, removeErr}))
	}

	return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeNormal, etcdMemberRemovedViaPeerEvent,
		fmt.Sprintf("etcd member %q removed via machine %q", member.Hostname, peer.Name))
}

// failOpenOrRetry either retries the cleanup or, once the deadline has passed, gives up and
// releases the hook anyway. A deletion is never parked forever: an orphaned etcd member is a
// problem auditEtcd can fix later, a Machine stuck in termination is not.
func (r *TalosControlPlaneReconciler) failOpenOrRetry(ctx context.Context, victim *clusterv1.Machine, cause error) (ctrl.Result, error) {
	if observedAt, ok := etcdCleanupObservedAt(victim); ok && time.Since(observedAt) > r.etcdCleanupTimeout() {
		r.Log.Error(cause, "etcd cleanup deadline exceeded, releasing the pre-terminate hook", "machine", victim.Name)

		return r.releasePreTerminateHookWithEvent(ctx, victim, corev1.EventTypeWarning, etcdCleanupOrphanedEvent,
			fmt.Sprintf("gave up removing the etcd member after %s, it may need to be removed manually: %v",
				r.etcdCleanupTimeout(), cause))
	}

	return ctrl.Result{RequeueAfter: preTerminateRequeueAfter},
		errors.Wrapf(cause, "failed to remove machine %q from etcd", victim.Name)
}

// releasePreTerminateHookWithEvent releases the hook and records why on the Machine.
func (r *TalosControlPlaneReconciler) releasePreTerminateHookWithEvent(
	ctx context.Context,
	machine *clusterv1.Machine,
	eventType, reason, message string,
) (ctrl.Result, error) {
	if err := r.releasePreTerminateHook(ctx, machine); err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info(message, "machine", machine.Name)
	r.recordMachineEvent(machine, eventType, reason, message)

	return ctrl.Result{}, nil
}

// releasePreTerminateHook removes the hook and its deadline anchor, letting the core Machine
// controller carry on with termination.
func (r *TalosControlPlaneReconciler) releasePreTerminateHook(ctx context.Context, machine *clusterv1.Machine) error {
	_, hooked := machine.Annotations[PreTerminateHookCleanupAnnotation]
	_, anchored := machine.Annotations[etcdCleanupObservedAtAnnotation]

	if !hooked && !anchored {
		return nil
	}

	patchHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return err
	}

	delete(machine.Annotations, PreTerminateHookCleanupAnnotation)
	delete(machine.Annotations, etcdCleanupObservedAtAnnotation)

	r.Log.Info("releasing pre-terminate hook", "machine", machine.Name)

	return errors.Wrapf(patchHelper.Patch(ctx, machine),
		"failed to release the pre-terminate hook on machine %q", machine.Name)
}

// stampPreTerminateHooks adopts owned control plane Machines that predate the hook.
//
// Machines that are already deleting are deliberately left alone: stamping one mid-pipeline
// races the phase gate — the Machine may be past the pre-terminate phase already — and buys
// nothing, since the etcd member of a machine that far along is auditEtcd's problem.
func (r *TalosControlPlaneReconciler) stampPreTerminateHooks(ctx context.Context, owned []*clusterv1.Machine) error {
	if !r.EnableMachinePreTerminateHook {
		return nil
	}

	var errs []error

	for _, machine := range owned {
		if !machine.DeletionTimestamp.IsZero() {
			continue
		}

		if _, hooked := machine.Annotations[PreTerminateHookCleanupAnnotation]; hooked {
			continue
		}

		patchHelper, err := patch.NewHelper(machine, r.Client)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if machine.Annotations == nil {
			machine.Annotations = map[string]string{}
		}

		machine.Annotations[PreTerminateHookCleanupAnnotation] = ""

		r.Log.Info("adding pre-terminate hook to control plane machine", "machine", machine.Name)

		if err := patchHelper.Patch(ctx, machine); err != nil {
			errs = append(errs, errors.Wrapf(err, "failed to add the pre-terminate hook to machine %q", machine.Name))
		}
	}

	return kerrors.NewAggregate(errs)
}

// stampEtcdCleanupObservedAt records the start of the fail-open window, once.
func (r *TalosControlPlaneReconciler) stampEtcdCleanupObservedAt(ctx context.Context, machine *clusterv1.Machine) error {
	if _, ok := etcdCleanupObservedAt(machine); ok {
		return nil
	}

	patchHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return err
	}

	if machine.Annotations == nil {
		machine.Annotations = map[string]string{}
	}

	machine.Annotations[etcdCleanupObservedAtAnnotation] = time.Now().UTC().Format(time.RFC3339)

	return errors.Wrapf(patchHelper.Patch(ctx, machine),
		"failed to anchor the etcd cleanup deadline on machine %q", machine.Name)
}

// markEtcdLeaving flags the machine so etcdHealthcheck stops expecting it to be a member.
func (r *TalosControlPlaneReconciler) markEtcdLeaving(ctx context.Context, machine *clusterv1.Machine) error {
	if machine.Annotations[etcdLeavingAnnotation] == "true" {
		return nil
	}

	patchHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return err
	}

	if machine.Annotations == nil {
		machine.Annotations = map[string]string{}
	}

	machine.Annotations[etcdLeavingAnnotation] = "true"

	return errors.Wrapf(patchHelper.Patch(ctx, machine),
		"failed to mark machine %q as leaving etcd", machine.Name)
}

// etcdLeaveFromVictim asks the machine being deleted to leave etcd on its own.
//
// Unlike the legacy inline path in scaleDownControlPlane, a stopped etcd is reported as a
// failure rather than a silent success: an etcd that is no longer running cannot remove itself
// from the member list, so the caller has to fall back to removing the member through a peer.
func (r *TalosControlPlaneReconciler) etcdLeaveFromVictim(ctx context.Context, tcp *controlplanev1.TalosControlPlane, victim *clusterv1.Machine) error {
	c, err := r.etcdClientFor(ctx, tcp, *victim)
	if err != nil {
		return err
	}

	defer c.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(ctx, etcdCallTimeout)
	defer cancel()

	svcs, err := c.ServiceInfo(ctx, "etcd")
	if err != nil {
		return err
	}

	for _, svc := range svcs {
		if svc.Service.GetState() == "Finished" {
			continue
		}

		r.Log.Info("forfeiting etcd leadership", "machine", victim.Name)

		if _, err := c.EtcdForfeitLeadership(ctx, &machineapi.EtcdForfeitLeadershipRequest{}); err != nil {
			return err
		}

		r.Log.Info("leaving etcd", "machine", victim.Name)

		return c.EtcdLeaveCluster(ctx, &machineapi.EtcdLeaveClusterRequest{})
	}

	return errors.Errorf("etcd is not running on machine %q", victim.Name)
}

// findEtcdMember looks the machine up in the etcd member list reported by a peer. A nil member
// with a nil error means the machine is not a member (anymore).
func findEtcdMember(ctx context.Context, c etcdCalls, victim *clusterv1.Machine) (*machineapi.EtcdMember, error) {
	hostname := machineHostName(victim)
	if hostname == "" {
		return nil, errors.Errorf("machine %q has no hostname to match against the etcd member list", victim.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, etcdCallTimeout)
	defer cancel()

	response, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
	if err != nil {
		return nil, err
	}

	for _, message := range response.Messages {
		for _, member := range message.Members {
			if strings.EqualFold(member.Hostname, hostname) {
				return member, nil
			}
		}
	}

	return nil, nil
}

// selectEtcdPeer picks a control plane machine that can speak for the etcd cluster on behalf of
// the machine being removed: owned, not the victim, not deleting, not already leaving etcd,
// preferring one that has a NodeRef.
func selectEtcdPeer(owned []*clusterv1.Machine, victim *clusterv1.Machine) *clusterv1.Machine {
	var fallback *clusterv1.Machine

	for _, machine := range owned {
		if machine.Name == victim.Name {
			continue
		}

		if !machine.DeletionTimestamp.IsZero() {
			continue
		}

		if machine.Annotations[etcdLeavingAnnotation] == "true" {
			continue
		}

		if machine.Status.NodeRef.IsDefined() {
			return machine
		}

		if fallback == nil {
			fallback = machine
		}
	}

	return fallback
}

// ownedControlPlaneMachines narrows a control plane machine list to the machines this
// TalosControlPlane actually controls, in a stable order.
func ownedControlPlaneMachines(tcp *controlplanev1.TalosControlPlane, machines *clusterv1.MachineList) []*clusterv1.Machine {
	owned := make([]*clusterv1.Machine, 0, len(machines.Items))

	for i := range machines.Items {
		machine := &machines.Items[i]

		if !metav1.IsControlledBy(machine, tcp) {
			continue
		}

		owned = append(owned, machine)
	}

	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })

	return owned
}

// deletingMachines splits the owned machines that are being deleted into the ones carrying our
// hook -- oldest deletionTimestamp first, name as the tie-break -- and the first one found
// without it. The caller is name-ordered, so the unhooked machine reported is deterministic.
func deletingMachines(owned []*clusterv1.Machine) (hooked []*clusterv1.Machine, unhooked *clusterv1.Machine) {
	deleting := []*clusterv1.Machine{}

	for _, machine := range owned {
		if machine.DeletionTimestamp.IsZero() {
			continue
		}

		if _, ok := machine.Annotations[PreTerminateHookCleanupAnnotation]; !ok {
			if unhooked == nil {
				unhooked = machine
			}

			continue
		}

		deleting = append(deleting, machine)
	}

	sort.SliceStable(deleting, func(i, j int) bool {
		if !deleting[i].DeletionTimestamp.Equal(deleting[j].DeletionTimestamp) {
			return deleting[i].DeletionTimestamp.Before(deleting[j].DeletionTimestamp)
		}

		return deleting[i].Name < deleting[j].Name
	})

	return deleting, unhooked
}

// etcdCleanupObservedAt reads the fail-open deadline anchor off the Machine.
func etcdCleanupObservedAt(machine *clusterv1.Machine) (time.Time, bool) {
	value, ok := machine.Annotations[etcdCleanupObservedAtAnnotation]
	if !ok {
		return time.Time{}, false
	}

	observedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}

	return observedAt, true
}

// recordMachineEvent records an event on the Machine, if a recorder is wired up.
func (r *TalosControlPlaneReconciler) recordMachineEvent(machine *clusterv1.Machine, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}

	r.Recorder.Event(machine, eventType, reason, message)
}
