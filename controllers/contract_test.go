// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"testing"

	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestContractAPIVersion checks that the apiVersion comes from the contract labels of the CRD, not from the
// versions the CRD serves.
func TestContractAPIVersion(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))

	gk := schema.GroupKind{Group: "infrastructure.cluster.x-k8s.io", Kind: "GenericInfrastructureMachineTemplate"}

	for _, tc := range []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name: "latest version of the v1beta2 contract",
			labels: map[string]string{
				"cluster.x-k8s.io/v1beta1": "v1alpha4",
				"cluster.x-k8s.io/v1beta2": "v1beta1_v1alpha5",
			},
			expected: "infrastructure.cluster.x-k8s.io/v1beta1",
		},
		{
			name: "falls back to the v1beta1 contract",
			labels: map[string]string{
				"cluster.x-k8s.io/v1beta1": "v1alpha4",
			},
			expected: "infrastructure.cluster.x-k8s.io/v1alpha4",
		},
		{
			name: "no contract labels",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "genericinfrastructuremachinetemplates.infrastructure.cluster.x-k8s.io",
					Labels: tc.labels,
				},
			}

			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()

			apiVersion, err := ContractAPIVersion(context.Background(), c, gk)
			if tc.expected == "" {
				g.Expect(err).To(HaveOccurred())

				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(apiVersion).To(Equal(tc.expected))
		})
	}
}

// TestContractAPIVersionGetter checks that the getter reads through the uncached reader while the cache is not
// started, and logs the failures the conversion degrades on.
func TestContractAPIVersionGetter(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))

	gk := schema.GroupKind{Group: "infrastructure.cluster.x-k8s.io", Kind: "GenericInfrastructureMachineTemplate"}

	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "genericinfrastructuremachinetemplates.infrastructure.cluster.x-k8s.io",
			Labels: map[string]string{"cluster.x-k8s.io/v1beta2": "v1beta1"},
		},
	}

	notStarted := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return &cache.ErrCacheNotStarted{}
		},
	}).Build()

	for _, tc := range []struct {
		name       string
		cached     client.Reader
		uncached   client.Reader
		expected   string
		expectLogs bool
	}{
		{
			name:     "cache started",
			cached:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build(),
			uncached: notStarted,
			expected: "infrastructure.cluster.x-k8s.io/v1beta1",
		},
		{
			name:     "cache not started",
			cached:   notStarted,
			uncached: fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build(),
			expected: "infrastructure.cluster.x-k8s.io/v1beta1",
		},
		{
			name:       "CRD not installed",
			cached:     fake.NewClientBuilder().WithScheme(scheme).Build(),
			uncached:   notStarted,
			expectLogs: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			var logs []string

			log := funcr.New(func(_, args string) { logs = append(logs, args) }, funcr.Options{})

			apiVersion, err := ContractAPIVersionGetter(tc.cached, tc.uncached, log)(gk)
			if tc.expectLogs {
				g.Expect(err).To(HaveOccurred())
				g.Expect(logs).To(HaveLen(1))

				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(apiVersion).To(Equal(tc.expected))
			g.Expect(logs).To(BeEmpty())
		})
	}
}
