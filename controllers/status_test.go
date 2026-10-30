// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// newStatusTestObjects returns a Cluster without a control plane endpoint, so that reconcile stops right after
// the infrastructure checks and only the status update runs, and a TalosControlPlane that already has the finalizer.
func newStatusTestObjects(name string, replicas int32) (*clusterv1.Cluster, *controlplanev1.TalosControlPlane) {
	cluster := newCluster(&types.NamespacedName{Name: name, Namespace: metav1.NamespaceDefault})
	cluster.Status.Initialization.InfrastructureProvisioned = pointer.Bool(true)

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  cluster.Namespace,
			Name:       name,
			Finalizers: []string{controlplanev1.TalosControlPlaneFinalizer},
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       cluster.Name,
				},
			},
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Replicas: pointer.Int32(replicas),
			Version:  "v1.16.6",
		},
	}

	return cluster, tcp
}

// validateConditionReasons mimics the apiserver validation of status.conditions[].reason, which the CRD schema
// generated for []metav1.Condition requires to be non-empty. The fake client doesn't validate against the schema.
//
// The whole object is validated, as an apiserver without CRD validation ratcheting does.
func validateConditionReasons(obj client.Object) error {
	tcp, ok := obj.(*controlplanev1.TalosControlPlane)
	if !ok {
		return nil
	}

	var errs field.ErrorList

	for i, condition := range tcp.Status.Conditions {
		if condition.Reason == "" {
			errs = append(errs, field.Invalid(field.NewPath("status", "conditions").Index(i).Child("reason"), "", "should be at least 1 chars long"))
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(controlplanev1.GroupVersion.WithKind("TalosControlPlane").GroupKind(), tcp.Name, errs)
}

func newValidatingFakeClient(initObjs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithObjects(initObjs...).
		WithScheme(fakeScheme).
		WithStatusSubresource(
			&clusterv1.Cluster{},
			&clusterv1.Machine{},
			&controlplanev1.TalosControlPlane{},
		).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if err := validateConditionReasons(obj); err != nil {
					return err
				}

				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if err := validateConditionReasons(obj); err != nil {
					return err
				}

				return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			},
		}).
		Build()
}

// TestConditionReasonSanitizer covers a TalosControlPlane upgraded from v0.5.x, whose stored conditions were
// v1beta1 conditions without a reason. Status patches must still succeed, and the empty reasons must be replaced.
func TestConditionReasonSanitizer(t *testing.T) {
	g := NewWithT(t)
	ctx := t.Context()

	cluster, tcp := newStatusTestObjects("sanitizer", 1)

	// v0.5.x stored v1beta1 conditions, where the reason is optional and MarkTrue leaves it empty.
	legacyTransitionTime := metav1.NewTime(metav1.Now().Add(-5 * time.Minute))
	tcp.Status.Conditions = []metav1.Condition{
		{Type: clusterv1.ReadyCondition, Status: metav1.ConditionTrue, LastTransitionTime: legacyTransitionTime},
		{Type: clusterv1.AvailableCondition, Status: metav1.ConditionTrue, LastTransitionTime: legacyTransitionTime},
		{Type: "LegacyCondition", Status: metav1.ConditionTrue, LastTransitionTime: legacyTransitionTime},
	}

	fakeClient := newValidatingFakeClient(cluster, tcp)

	// The validation is in place: an empty reason is rejected.
	stored := &controlplanev1.TalosControlPlane{}
	g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), stored)).To(Succeed())
	g.Expect(stored.Status.Conditions).To(HaveLen(3))
	g.Expect(apierrors.IsInvalid(fakeClient.Status().Update(ctx, stored))).To(BeTrue())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	for range 2 {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
		g.Expect(err).NotTo(HaveOccurred())
	}

	g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), stored)).To(Succeed())

	for _, condition := range stored.Status.Conditions {
		g.Expect(condition.Reason).NotTo(BeEmpty(), "condition %s has no reason", condition.Type)
	}

	// Conditions the controller no longer sets get a placeholder reason, the others their real reason.
	expectCondition(g, stored, "LegacyCondition", metav1.ConditionTrue, "NoReasonReported")
	expectCondition(g, stored, clusterv1.AvailableCondition, metav1.ConditionFalse, controlplanev1.NotAvailableReason)
	g.Expect(conditions.Get(stored, clusterv1.ReadyCondition).Reason).NotTo(Equal("NoReasonReported"))
	expectCondition(g, stored, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)
}

// TestPausedConditionFollowsGeneration checks that the Paused condition of an unpaused TalosControlPlane gets the
// new generation after a spec change, although only its observed generation changes.
func TestPausedConditionFollowsGeneration(t *testing.T) {
	g := NewWithT(t)
	ctx := t.Context()

	cluster, tcp := newStatusTestObjects("paused-generation", 1)
	tcp.Generation = 2
	tcp.Status.Conditions = []metav1.Condition{
		{
			Type:               clusterv1.PausedCondition,
			Status:             metav1.ConditionFalse,
			Reason:             clusterv1.NotPausedReason,
			ObservedGeneration: 1,
			LastTransitionTime: metav1.NewTime(metav1.Now().Add(-5 * time.Minute)),
		},
	}

	fakeClient := newValidatingFakeClient(cluster, tcp)

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	stored := &controlplanev1.TalosControlPlane{}
	g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), stored)).To(Succeed())
	g.Expect(stored.Generation).To(BeEquivalentTo(2))

	expectCondition(g, stored, clusterv1.PausedCondition, metav1.ConditionFalse, clusterv1.NotPausedReason)
	g.Expect(conditions.Get(stored, clusterv1.PausedCondition).ObservedGeneration).To(BeEquivalentTo(2))
}

// failingProbeClusterCache reports that the health probe of every workload cluster last succeeded at lastProbeSuccess.
type failingProbeClusterCache struct {
	clustercache.ClusterCache

	lastProbeSuccess time.Time
}

func (c failingProbeClusterCache) GetHealthCheckingState(context.Context, client.ObjectKey) clustercache.HealthCheckingState {
	return clustercache.HealthCheckingState{
		LastProbeTime:        time.Now(),
		LastProbeSuccessTime: c.lastProbeSuccess,
		ConsecutiveFailures:  10,
	}
}

// TestStatusWorkloadClusterUnreachable checks that the Available condition reflects that the workload cluster
// can't be reached, while the replica counters keep being computed from the Machines in the management cluster.
func TestStatusWorkloadClusterUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		// initialized sets the contract initialization field and the deprecated v1beta1 one.
		initialized bool
		// previouslyAvailable sets Available=True before the connection was lost.
		previouslyAvailable bool
		// lastProbeSuccess, if set, is how long ago the ClusterCache health probe last succeeded.
		lastProbeSuccess time.Duration
		availableStatus  metav1.ConditionStatus
		availableReason  string
	}{
		{
			name:            "initialized",
			initialized:     true,
			availableStatus: metav1.ConditionUnknown,
			availableReason: controlplanev1.RemoteConnectionProbeFailedReason,
		},
		{
			// e.g. right after a controller restart, before the ClusterCache connected.
			name:                "within the grace period",
			initialized:         true,
			previouslyAvailable: true,
			availableStatus:     metav1.ConditionTrue,
			availableReason:     controlplanev1.AvailableReason,
		},
		{
			name:                "probe failing within the grace period",
			initialized:         true,
			previouslyAvailable: true,
			lastProbeSuccess:    time.Minute,
			availableStatus:     metav1.ConditionTrue,
			availableReason:     controlplanev1.AvailableReason,
		},
		{
			name:                "probe failing longer than the grace period",
			initialized:         true,
			previouslyAvailable: true,
			lastProbeSuccess:    time.Hour,
			availableStatus:     metav1.ConditionUnknown,
			availableReason:     controlplanev1.RemoteConnectionProbeFailedReason,
		},
		{
			name:            "not initialized",
			availableStatus: metav1.ConditionFalse,
			availableReason: controlplanev1.NotAvailableReason,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := t.Context()

			cluster, tcp := newStatusTestObjects("unreachable", 3)

			if tc.initialized {
				tcp.V1Beta1DeprecatedStatus().Initialized = true
				tcp.Status.Initialization.ControlPlaneInitialized = pointer.Bool(true)
			}

			if tc.previouslyAvailable {
				conditions.Set(tcp, metav1.Condition{
					Type:   clusterv1.AvailableCondition,
					Status: metav1.ConditionTrue,
					Reason: controlplanev1.AvailableReason,
				})
			}

			objs := []client.Object{cluster, tcp, GenericInfrastructureMachineCRD.DeepCopy()}

			// Two ready and available Machines, and one that is still provisioning.
			for i, ready := range []bool{true, true, false} {
				machine, _ := createMachineNodePair(fmt.Sprintf("unreachable-%d", i), cluster, tcp, ready, "")
				objs = append(objs, machine)
			}

			fakeClient := newFakeClient(objs...)

			// No workload cluster is registered in the cluster cache, so getting a client for it fails.
			r := newReconciler(fakeClient)

			if tc.lastProbeSuccess != 0 {
				r.ClusterCache = failingProbeClusterCache{
					ClusterCache:     r.ClusterCache,
					lastProbeSuccess: time.Now().Add(-tc.lastProbeSuccess),
				}
			}

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), tcp)).To(Succeed())

			expectCondition(g, tcp, clusterv1.AvailableCondition, tc.availableStatus, tc.availableReason)

			// A lost connection doesn't reset the initialization.
			g.Expect(tcp.V1Beta1DeprecatedStatus().Initialized).To(Equal(tc.initialized))

			if tc.initialized {
				g.Expect(tcp.Status.Initialization.ControlPlaneInitialized).To(HaveValue(BeTrue()))
			} else {
				g.Expect(tcp.Status.Initialization.ControlPlaneInitialized).To(BeNil())
			}

			if tc.availableStatus == metav1.ConditionUnknown {
				g.Expect(conditions.Get(tcp, clusterv1.AvailableCondition).Message).To(ContainSubstring("Workload cluster can't be reached"))
			}

			g.Expect(tcp.Status.Replicas).To(HaveValue(BeEquivalentTo(3)))
			g.Expect(tcp.Status.ReadyReplicas).To(HaveValue(BeEquivalentTo(2)))
			g.Expect(tcp.Status.AvailableReplicas).To(HaveValue(BeEquivalentTo(2)))
			g.Expect(tcp.Status.UpToDateReplicas).To(HaveValue(BeEquivalentTo(3)))
			g.Expect(tcp.V1Beta1DeprecatedStatus().UnavailableReplicas).To(BeEquivalentTo(1))
			g.Expect(tcp.V1Beta1DeprecatedStatus().Ready).To(BeTrue())
			expectCondition(g, tcp, clusterv1.MachinesReadyCondition, metav1.ConditionFalse, controlplanev1.MachinesNotReadyReason)
			expectCondition(g, tcp, clusterv1.MachinesUpToDateCondition, metav1.ConditionTrue, controlplanev1.MachinesUpToDateReason)
		})
	}
}

// TestStatusAvailableWithoutReadyMachines checks that the control plane is Available as soon as the workload cluster
// API can be reached, even if no Machine is Ready yet, e.g. because the Nodes wait for a CNI to be installed.
func TestStatusAvailableWithoutReadyMachines(t *testing.T) {
	g := NewWithT(t)
	ctx := t.Context()

	cluster, tcp := newStatusTestObjects("no-ready-machines", 1)

	machine, node := createMachineNodePair("no-ready-machines-0", cluster, tcp, false, "")

	fakeClient := newFakeClient(cluster, tcp, machine, node, GenericInfrastructureMachineCRD.DeepCopy())

	r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), tcp)).To(Succeed())

	g.Expect(tcp.Status.Initialization.ControlPlaneInitialized).To(HaveValue(BeTrue()))
	g.Expect(tcp.Status.ReadyReplicas).To(HaveValue(BeEquivalentTo(0)))
	expectCondition(g, tcp, clusterv1.AvailableCondition, metav1.ConditionTrue, controlplanev1.AvailableReason)
	expectCondition(g, tcp, clusterv1.MachinesReadyCondition, metav1.ConditionFalse, controlplanev1.MachinesNotReadyReason)
}

// TestInfrastructureProvisionedGate checks that reconciliation waits for status.initialization.infrastructureProvisioned
// rather than the InfrastructureReady condition, and that deletion is never blocked by it.
func (suite *ControllersSuite) TestInfrastructureProvisionedGate() {
	for _, tc := range []struct {
		name                      string
		namespace                 string
		infrastructureProvisioned *bool
		infrastructureReady       metav1.ConditionStatus
		expectReconciled          bool
	}{
		{
			name:                      "provisioned, InfrastructureReady False",
			namespace:                 "test-infra-gate-provisioned",
			infrastructureProvisioned: pointer.Bool(true),
			infrastructureReady:       metav1.ConditionFalse,
			expectReconciled:          true,
		},
		{
			name:                "not provisioned, InfrastructureReady True",
			namespace:           "test-infra-gate-not-provisioned",
			infrastructureReady: metav1.ConditionTrue,
		},
	} {
		suite.Run(tc.name, func() {
			g := NewWithT(suite.T())
			ctx := suite.ctx

			fakeClient := newFakeClient()

			cluster, tcp, infrastructureMachineTemplate := suite.setupCluster(fakeClient, tc.namespace, pointer.Int32(1))

			g.Expect(fakeClient.Get(ctx, util.ObjectKey(cluster), cluster)).To(Succeed())

			cluster.Status.Initialization.InfrastructureProvisioned = tc.infrastructureProvisioned
			cluster.Status.Conditions = []metav1.Condition{
				{
					Type:               clusterv1.InfrastructureReadyCondition,
					Status:             tc.infrastructureReady,
					Reason:             "Test",
					LastTransitionTime: metav1.Now(),
				},
			}
			g.Expect(fakeClient.Status().Update(ctx, cluster)).To(Succeed())

			r := newReconciler(fakeClient, withCluster(util.ObjectKey(cluster)))

			// The first reconcile adds the finalizer, the second one goes through the gate.
			for range 2 {
				_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
				g.Expect(err).NotTo(HaveOccurred())
			}

			g.Expect(fakeClient.Get(ctx, util.ObjectKey(infrastructureMachineTemplate), infrastructureMachineTemplate)).To(Succeed())

			machines := &clusterv1.MachineList{}
			g.Expect(fakeClient.List(ctx, machines, client.InNamespace(cluster.Namespace))).To(Succeed())

			if tc.expectReconciled {
				g.Expect(infrastructureMachineTemplate.GetOwnerReferences()).NotTo(BeEmpty(), "reconciliation must not wait for the InfrastructureReady condition")
				g.Expect(machines.Items).To(HaveLen(1), "reconciliation must not wait for the InfrastructureReady condition")
			} else {
				g.Expect(infrastructureMachineTemplate.GetOwnerReferences()).To(BeEmpty(), "reconciliation must wait for infrastructureProvisioned")
				g.Expect(machines.Items).To(BeEmpty(), "reconciliation must wait for infrastructureProvisioned")

				machine, _ := createMachineNodePair("infra-gate-0", cluster, tcp, true, "")
				g.Expect(fakeClient.Create(ctx, machine)).To(Succeed())
			}

			// Deletion goes ahead regardless of the infrastructure state.
			g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
			g.Expect(fakeClient.Delete(ctx, tcp)).To(Succeed())

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(fakeClient.Get(ctx, util.ObjectKey(tcp), tcp)).To(Succeed())
			expectCondition(g, tcp, clusterv1.AvailableCondition, metav1.ConditionFalse, controlplanev1.NotAvailableReason)
			expectCondition(g, tcp, string(controlplanev1.ResizedCondition), metav1.ConditionFalse, clusterv1.DeletingReason)

			g.Expect(fakeClient.List(ctx, machines, client.InNamespace(cluster.Namespace))).To(Succeed())
			g.Expect(machines.Items).To(BeEmpty())

			_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(tcp)})
			g.Expect(err).NotTo(HaveOccurred())

			g.Expect(apierrors.IsNotFound(fakeClient.Get(ctx, util.ObjectKey(tcp), tcp))).To(BeTrue(), "the finalizer must be removed")
		})
	}
}
