// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// TestInfrastructureTemplateAPIVersionResolution checks that a TalosControlPlane that pins an older apiVersion of its
// infrastructure template uses the apiVersion the template CRD maps to the contract, to set the owner reference and
// to clone the template.
func TestInfrastructureTemplateAPIVersionResolution(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	const (
		templateGroup = "infrastructure.cluster.x-k8s.io"
		templateKind  = "GenericInfrastructureMachineTemplate"
	)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clusterv1.AddToScheme,
		controlplanev1.AddToScheme,
		cabptv1.AddToScheme,
		corev1.AddToScheme,
		apiextensionsv1.AddToScheme,
	} {
		g.Expect(add(scheme)).To(Succeed())
	}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: contract.CalculateCRDName(templateGroup, templateKind),
			Labels: map[string]string{
				"cluster.x-k8s.io/v1beta2": "v1beta1_v1beta2",
			},
		},
	}

	// The template only exists in the resolved version: getting it through the pinned version fails.
	template := &unstructured.Unstructured{}
	template.SetAPIVersion(templateGroup + "/v1beta2")
	template.SetKind(templateKind)
	template.SetNamespace(remediationTestNamespace)
	template.SetName("infra")
	g.Expect(unstructured.SetNestedField(template.Object, map[string]any{"hello": "world"}, "spec", "template", "spec")).To(Succeed())

	cluster := newTestCluster()
	cluster.UID = "cluster-uid"

	inProgress, err := (&remediationData{Machine: "m0", Timestamp: metav1.Now()}).marshal()
	g.Expect(err).NotTo(HaveOccurred())

	tcp := newTestTCP(3)
	tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress}
	tcp.Spec.MachineTemplate.Spec.InfrastructureRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: templateGroup,
		Kind:     templateKind,
		Name:     "infra",
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd, template, cluster, tcp).Build()

	r := &TalosControlPlaneReconciler{
		Client:    c,
		APIReader: c,
		Log:       testr.New(t),
		Scheme:    scheme,
	}

	g.Expect(r.reconcileExternalReference(ctx, tcp.Spec.MachineTemplate.Spec.InfrastructureRef, cluster)).To(Succeed())

	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(template), template)).To(Succeed())
	g.Expect(template.GetOwnerReferences()).To(ContainElement(HaveField("UID", cluster.UID)))

	_, err = r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster}, false)
	g.Expect(err).NotTo(HaveOccurred())

	machines := &clusterv1.MachineList{}
	g.Expect(c.List(ctx, machines, client.InNamespace(remediationTestNamespace))).To(Succeed())
	g.Expect(machines.Items).To(HaveLen(1))

	machine := machines.Items[0]
	g.Expect(machine.Spec.InfrastructureRef.APIGroup).To(Equal(templateGroup))
	g.Expect(machine.Spec.InfrastructureRef.Kind).To(Equal("GenericInfrastructureMachine"))

	infraMachine := &unstructured.Unstructured{}
	infraMachine.SetAPIVersion(templateGroup + "/v1beta2")
	infraMachine.SetKind("GenericInfrastructureMachine")
	g.Expect(c.Get(ctx, client.ObjectKey{Namespace: remediationTestNamespace, Name: machine.Spec.InfrastructureRef.Name}, infraMachine)).To(Succeed())

	// A Machine created while a remediation is in progress records the remediation it replaces.
	g.Expect(machine.Annotations).To(HaveKeyWithValue(remediationForAnnotation, inProgress))
}
