// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/gomega"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/contract"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

const (
	bootTestTemplateGroup = "infrastructure.cluster.x-k8s.io"
	bootTestTemplateKind  = "GenericInfrastructureMachineTemplate"
)

// bootTestObjects returns the scheme, the Cluster and the TalosControlPlane to boot a Machine with, and the
// infrastructure template CRD and template it clones. The template only exists in the version its CRD maps to the
// contract, v1beta2.
func bootTestObjects(g *WithT) (*runtime.Scheme, *clusterv1.Cluster, *controlplanev1.TalosControlPlane, []client.Object) {
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
			Name: contract.CalculateCRDName(bootTestTemplateGroup, bootTestTemplateKind),
			Labels: map[string]string{
				"cluster.x-k8s.io/v1beta2": "v1beta1_v1beta2",
			},
		},
	}

	template := &unstructured.Unstructured{}
	template.SetAPIVersion(bootTestTemplateGroup + "/v1beta2")
	template.SetKind(bootTestTemplateKind)
	template.SetNamespace(remediationTestNamespace)
	template.SetName("infra")
	g.Expect(unstructured.SetNestedField(template.Object, map[string]any{"hello": "world"}, "spec", "template", "spec")).To(Succeed())

	cluster := newTestCluster()
	cluster.UID = "cluster-uid"

	tcp := newTestTCP(3)
	tcp.Spec.MachineTemplate.Spec.InfrastructureRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: bootTestTemplateGroup,
		Kind:     bootTestTemplateKind,
		Name:     "infra",
	}

	return scheme, cluster, tcp, []client.Object{crd, template}
}

// listBootTestClones returns the infrastructure machines and the TalosConfigs cloned by the boot of a Machine.
func listBootTestClones(ctx context.Context, g *WithT, c client.Client) ([]unstructured.Unstructured, []cabptv1.TalosConfig) {
	infraMachines := &unstructured.UnstructuredList{}
	infraMachines.SetAPIVersion(bootTestTemplateGroup + "/v1beta2")
	infraMachines.SetKind("GenericInfrastructureMachineList")
	g.Expect(c.List(ctx, infraMachines, client.InNamespace(remediationTestNamespace))).To(Succeed())

	talosConfigs := &cabptv1.TalosConfigList{}
	g.Expect(c.List(ctx, talosConfigs, client.InNamespace(remediationTestNamespace))).To(Succeed())

	return infraMachines.Items, talosConfigs.Items
}

// TestInfrastructureTemplateAPIVersionResolution checks that a TalosControlPlane that pins an older apiVersion of its
// infrastructure template uses the apiVersion the template CRD maps to the contract, to set the owner reference and
// to clone the template, and that the cloned objects get the labels and annotations of the Machine.
func TestInfrastructureTemplateAPIVersionResolution(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	scheme, cluster, tcp, objs := bootTestObjects(g)
	template := objs[1]

	inProgress, err := (&remediationData{Machine: "m0", Timestamp: metav1.Now()}).marshal()
	g.Expect(err).NotTo(HaveOccurred())

	tcp.Annotations = map[string]string{remediationInProgressAnnotation: inProgress}
	tcp.Spec.MachineTemplate.ObjectMeta = clusterv1.ObjectMeta{
		Labels:      map[string]string{"example.siderolabs.dev/role": "cp"},
		Annotations: map[string]string{"example.siderolabs.dev/note": "hello"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp)...).Build()

	r := &TalosControlPlaneReconciler{
		Client:    c,
		APIReader: c,
		Log:       testr.New(t),
		Scheme:    scheme,
	}

	infraTemplate, err := r.getInfrastructureTemplate(ctx, tcp)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(infraTemplate.GetAPIVersion()).To(Equal(bootTestTemplateGroup + "/v1beta2"))

	g.Expect(r.reconcileExternalReference(ctx, infraTemplate, cluster)).To(Succeed())

	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(template), template)).To(Succeed())
	g.Expect(template.GetOwnerReferences()).To(ContainElement(HaveField("UID", cluster.UID)))

	// The Machine is booted from the template the reconcile resolved.
	_, err = r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster, infraTemplate: infraTemplate}, false)
	g.Expect(err).NotTo(HaveOccurred())

	machines := &clusterv1.MachineList{}
	g.Expect(c.List(ctx, machines, client.InNamespace(remediationTestNamespace))).To(Succeed())
	g.Expect(machines.Items).To(HaveLen(1))

	machine := machines.Items[0]
	g.Expect(machine.Spec.InfrastructureRef.APIGroup).To(Equal(bootTestTemplateGroup))
	g.Expect(machine.Spec.InfrastructureRef.Kind).To(Equal("GenericInfrastructureMachine"))

	infraMachine := &unstructured.Unstructured{}
	infraMachine.SetAPIVersion(bootTestTemplateGroup + "/v1beta2")
	infraMachine.SetKind("GenericInfrastructureMachine")
	g.Expect(c.Get(ctx, client.ObjectKey{Namespace: remediationTestNamespace, Name: machine.Spec.InfrastructureRef.Name}, infraMachine)).To(Succeed())

	talosConfig := &cabptv1.TalosConfig{}
	g.Expect(c.Get(ctx, client.ObjectKey{Namespace: remediationTestNamespace, Name: machine.Spec.Bootstrap.ConfigRef.Name}, talosConfig)).To(Succeed())

	for _, obj := range []client.Object{infraMachine, talosConfig} {
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.ClusterNameLabel, cluster.Name))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.MachineControlPlaneLabel, ""))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue(clusterv1.MachineControlPlaneNameLabel, tcp.Name))
		g.Expect(obj.GetLabels()).To(HaveKeyWithValue("example.siderolabs.dev/role", "cp"))
		g.Expect(obj.GetAnnotations()).To(HaveKeyWithValue("example.siderolabs.dev/note", "hello"))
	}

	// A Machine created while a remediation is in progress records the remediation it replaces.
	g.Expect(machine.Annotations).To(HaveKeyWithValue(remediationForAnnotation, inProgress))
}

// TestBootControlPlaneRejectsExistingMachineName checks that a generated name that is already taken by a Machine
// fails the boot, instead of taking over that Machine, and that nothing is cloned for it.
func TestBootControlPlaneRejectsExistingMachineName(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	scheme, cluster, tcp, objs := bootTestObjects(g)
	tcp.Spec.MachineNamingStrategy = &controlplanev1.MachineNamingStrategy{Template: "{{ .talosControlPlane.name }}-cp"}

	existing := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: remediationTestNamespace,
			Name:      tcp.Name + "-cp",
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: bootTestTemplateGroup,
				Kind:     "GenericInfrastructureMachine",
				Name:     "existing-infra",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp, existing)...).Build()

	r := &TalosControlPlaneReconciler{
		Client:    c,
		APIReader: c,
		Log:       testr.New(t),
		Scheme:    scheme,
	}

	_, err := r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster}, false)
	g.Expect(err).To(MatchError(ContainSubstring("already exists")))

	condition := apimeta.FindStatusCondition(tcp.Status.Conditions, string(controlplanev1.MachinesCreatedCondition))
	g.Expect(condition).NotTo(BeNil())
	g.Expect(condition.Reason).To(Equal(controlplanev1.MachineGenerationFailedReason))

	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(existing), existing)).To(Succeed())
	g.Expect(existing.Spec.InfrastructureRef.Name).To(Equal("existing-infra"))

	infraMachines, talosConfigs := listBootTestClones(ctx, g, c)
	g.Expect(infraMachines).To(BeEmpty())
	g.Expect(talosConfigs).To(BeEmpty())
}

// TestBootControlPlaneCleansUpAfterFailure checks that the objects cloned for a Machine that could not be created
// are deleted, so that each failed attempt does not leave them behind.
func TestBootControlPlaneCleansUpAfterFailure(t *testing.T) {
	errInjected := errors.New("injected failure")

	for _, tc := range []struct {
		name  string
		funcs interceptor.Funcs
	}{
		{
			name: "TalosConfig creation fails",
			funcs: interceptor.Funcs{
				Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
					if appliedKind(obj) == "TalosConfig" {
						return errInjected
					}

					return c.Apply(ctx, obj, opts...)
				},
			},
		},
		{
			name: "Machine apply fails",
			funcs: interceptor.Funcs{
				Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
					if appliedKind(obj) == "Machine" {
						return errInjected
					}

					return c.Apply(ctx, obj, opts...)
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := context.Background()

			scheme, cluster, tcp, objs := bootTestObjects(g)

			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp)...).WithInterceptorFuncs(tc.funcs).Build()

			r := &TalosControlPlaneReconciler{
				Client:    c,
				APIReader: c,
				Log:       testr.New(t),
				Scheme:    scheme,
			}

			_, err := r.bootControlPlane(ctx, cluster, tcp, &ControlPlane{TCP: tcp, Cluster: cluster}, false)
			g.Expect(err).To(MatchError(ContainSubstring(errInjected.Error())))

			infraMachines, talosConfigs := listBootTestClones(ctx, g, c)
			g.Expect(infraMachines).To(BeEmpty())
			g.Expect(talosConfigs).To(BeEmpty())
		})
	}
}

// appliedKind returns the kind of an applied object.
func appliedKind(obj runtime.ApplyConfiguration) string {
	content, err := json.Marshal(obj)
	if err != nil {
		return ""
	}

	var typeMeta metav1.TypeMeta
	if err := json.Unmarshal(content, &typeMeta); err != nil {
		return ""
	}

	return typeMeta.Kind
}

// TestInfrastructureTemplateFailedReportsFailures checks that a template that cannot be resolved is reported in the
// MachinesCreated condition: a missing template backs off, while a template whose CRD has no contract version labels
// fails the reconcile.
func TestInfrastructureTemplateFailedReportsFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutate         func(objs []client.Object, tcp *controlplanev1.TalosControlPlane)
		expectedReason string
		expectError    bool
	}{
		{
			name: "template not found",
			mutate: func(_ []client.Object, tcp *controlplanev1.TalosControlPlane) {
				tcp.Spec.MachineTemplate.Spec.InfrastructureRef.Name = "missing"
			},
			expectedReason: controlplanev1.InfrastructureTemplateCloningFailedReason,
		},
		{
			name: "CRD without contract labels",
			mutate: func(objs []client.Object, _ *controlplanev1.TalosControlPlane) {
				objs[0].SetLabels(nil)
			},
			expectedReason: controlplanev1.InfrastructureTemplateResolutionFailedReason,
			expectError:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := context.Background()

			scheme, cluster, tcp, objs := bootTestObjects(g)
			tc.mutate(objs, tcp)

			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cluster, tcp)...).Build()
			r := &TalosControlPlaneReconciler{Client: c, APIReader: c, Log: testr.New(t), Scheme: scheme}

			_, err := r.getInfrastructureTemplate(ctx, tcp)
			g.Expect(err).To(HaveOccurred())

			res, err := infrastructureTemplateFailed(tcp, err)
			if tc.expectError {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(res.RequeueAfter).NotTo(BeZero())
			}

			condition := apimeta.FindStatusCondition(tcp.Status.Conditions, string(controlplanev1.MachinesCreatedCondition))
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(condition.Reason).To(Equal(tc.expectedReason))
		})
	}
}
