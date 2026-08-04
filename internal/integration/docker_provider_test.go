// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package integration_test contains core runners for integration tests
package integration_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/siderolabs/capi-utils/pkg/capi"
	"github.com/siderolabs/go-retry/retry"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"

	"github.com/siderolabs/capi-utils/pkg/capi/infrastructure"
)

const (
	dockerProviderName      = "docker"
	dockerProviderNamespace = "capd-system"
	dockerControllerName    = "capd-controller-manager"
)

// DockerProvider implements infrastructure.Provider for CAPD (Cluster API Provider Docker).
// It satisfies the same interface as AWSProvider so it can be used directly with capi.Manager.
type DockerProvider struct {
	ProviderVersion string
	ProviderNS      string
	WatchingNS      string
}

// NewDockerProvider creates a new DockerProvider.
// version is the CAPD version string (e.g. "v1.12.2").
func NewDockerProvider(version string) *DockerProvider {
	return &DockerProvider{
		ProviderVersion: version,
		ProviderNS:      dockerProviderNamespace,
	}
}

// Name implements infrastructure.Provider.
func (d *DockerProvider) Name() string {
	return dockerProviderName
}

// Namespace implements infrastructure.Provider.
func (d *DockerProvider) Namespace() string {
	return d.ProviderNS
}

// Version implements infrastructure.Provider.
func (d *DockerProvider) Version() string {
	return d.ProviderVersion
}

// WatchingNamespace implements infrastructure.Provider.
func (d *DockerProvider) WatchingNamespace() string {
	return d.WatchingNS
}

// Configure implements infrastructure.Provider.
// CAPD needs no credentials — this is a no-op.
func (d *DockerProvider) Configure(_ any) error {
	return nil
}

// ProviderVars implements infrastructure.Provider.
// CAPD needs no special installation variables.
func (d *DockerProvider) ProviderVars() (infrastructure.Variables, error) {
	return infrastructure.Variables{}, nil
}

// ClusterVars implements infrastructure.Provider.
// Returns variables required by the CAPD cluster template.
// The docker/standard template needs no provider-specific variables beyond the
// common ones (CLUSTER_NAME, KUBERNETES_VERSION, etc.) that capi.Manager sets.
func (d *DockerProvider) ClusterVars(_ any) (infrastructure.Variables, error) {
	return infrastructure.Variables{}, nil
}

// IsInstalled implements infrastructure.Provider.
// Returns true when the capd-system namespace and capd-controller-manager deployment exist.
func (d *DockerProvider) IsInstalled(ctx context.Context, clientset *kubernetes.Clientset) (bool, error) {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, d.Namespace(), metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	if _, err = clientset.AppsV1().Deployments(d.Namespace()).Get(ctx, dockerControllerName, metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// GetClusterTemplate implements infrastructure.Provider.
func (d *DockerProvider) GetClusterTemplate(c client.Client, opts client.GetClusterTemplateOptions) (client.Template, error) {
	return c.GetClusterTemplate(context.TODO(), opts)
}

// WaitReady implements infrastructure.Provider.
// Polls until the capd-controller-manager deployment has all replicas ready.
func (d *DockerProvider) WaitReady(ctx context.Context, clientset *kubernetes.Clientset) error {
	return retry.Constant(10*time.Minute, retry.WithUnits(10*time.Second), retry.WithErrorLogging(true)).Retry(func() error {
		if _, err := clientset.CoreV1().Namespaces().Get(ctx, d.Namespace(), metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		var (
			err        error
			deployment *v1.Deployment
		)

		if deployment, err = clientset.AppsV1().Deployments(d.Namespace()).Get(ctx, dockerControllerName, metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		if deployment.Status.ReadyReplicas != deployment.Status.Replicas || deployment.Status.ReadyReplicas == 0 {
			return retry.ExpectedError(fmt.Errorf("capd: %d of %d replicas ready",
				deployment.Status.ReadyReplicas, deployment.Status.Replicas))
		}

		return nil
	})
}

// injectDockerProvider uses reflection to inject provider into the private
// capi.Manager.providers field.
//
// Background: FetchState (called inside Manager.Install) calls
// infrastructure.NewProvider for every provider found on the cluster.  Because
// the bephinix fork only recognises "aws", it silently skips the CAPD provider
// and leaves Manager.providers empty, causing DeployCluster to fail with
// "no infrastructure providers are installed".
//
// This shim re-injects our DockerProvider after Install() completes so the
// field contains the correct value before DeployCluster is called.
func injectDockerProvider(t *testing.T, manager *capi.Manager, provider infrastructure.Provider) {
	t.Helper()

	v := reflect.ValueOf(manager).Elem()
	f := v.FieldByName("providers")

	if !f.IsValid() {
		t.Fatal("capi.Manager.providers field not found via reflection — struct layout may have changed")
	}

	// providers is []infrastructure.Provider (unexported) — use unsafe to make it settable.
	f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	f.Set(reflect.ValueOf([]infrastructure.Provider{provider}))
}
