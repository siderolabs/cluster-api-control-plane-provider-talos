// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// TestReconcileBacksOffOnMissingInfrastructureTemplate checks that a missing infrastructure template is reported
// in the MachinesCreated condition and requeued, instead of failing the reconcile.
func TestReconcileBacksOffOnMissingInfrastructureTemplate(t *testing.T) {
	g := NewWithT(t)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	// Only the metadata of the CRD is read, to resolve the API version of the template from its contract label.
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "genericinfrastructuremachinetemplates.infrastructure.cluster.x-k8s.io",
			Labels: map[string]string{
				clusterv1.GroupVersion.String(): "v1beta2",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
	reconciler := &TalosControlPlaneReconciler{Client: fakeClient}

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workload-cluster",
			Namespace: "default",
		},
		Status: clusterv1.ClusterStatus{
			Initialization: clusterv1.ClusterInitializationStatus{
				InfrastructureProvisioned: ptr.To(true),
			},
		},
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "talos-control-plane",
			Namespace: "default",
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "infrastructure.cluster.x-k8s.io",
						Kind:     "GenericInfrastructureMachineTemplate",
						Name:     "missing",
					},
				},
			},
		},
	}

	res, _, err := reconciler.reconcile(context.Background(), cluster, tcp)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.RequeueAfter).To(Equal(30 * time.Second))

	condition := apimeta.FindStatusCondition(tcp.Status.Conditions, string(controlplanev1.MachinesCreatedCondition))
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	g.Expect(condition.Reason).To(Equal(controlplanev1.InfrastructureTemplateCloningFailedReason))
}
