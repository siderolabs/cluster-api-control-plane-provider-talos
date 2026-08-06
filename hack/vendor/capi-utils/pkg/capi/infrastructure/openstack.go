// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package infrastructure

import (
	"context"
	"fmt"
	"time"

	"github.com/siderolabs/go-retry/retry"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"

	"github.com/siderolabs/capi-utils/pkg/constants"
)

// NewOpenStackProvider creates new OpenStack infrastructure provider.
func NewOpenStackProvider(version, providerNS, watchingNS string) (*OpenStackProvider, error) {
	if providerNS == "" {
		providerNS = constants.OpenStackCAPONamespace
	}

	return &OpenStackProvider{
		ProviderVersion: version,
		ProviderNS:      providerNS,
		WatchingNS:      watchingNS,
	}, nil
}

// OpenStackProvider infrastructure provider.
type OpenStackProvider struct {
	CloudYAMLB64    string
	CACertB64       string
	ProviderVersion string
	ProviderNS      string
	WatchingNS      string
}

// NewOpenStackSetupOptions creates new OpenStackSetupOptions.
func NewOpenStackSetupOptions() *OpenStackSetupOptions {
	return &OpenStackSetupOptions{}
}

// OpenStackSetupOptions OpenStack specific setup options.
type OpenStackSetupOptions struct {
	// CloudYAML is the base64 encoded content of a clouds.yaml file used by
	// both clusterctl (via OPENSTACK_CLOUD_YAML_B64) and the workload
	// cluster's cloud-config Secret.
	CloudYAML string
	// CACert is the base64 encoded CA certificate bundle for the OpenStack
	// endpoints, when they are served over TLS with a private CA.
	CACert                     string
	OpenStackProviderNamespace string
	OpenStackWatchingNamespace string
}

// OpenStackDeployOptions defines provider specific settings for cluster deployment.
type OpenStackDeployOptions struct {
	CloudName                 string
	ExternalNetworkID         string
	ImageName                 string
	FailureDomain             string
	DNSNameservers            string
	NodeCIDR                  string
	ControlPlaneMachineFlavor string
	NodeMachineFlavor         string
	CloudProviderVersion      string
	CalicoVersion             string
}

// NewOpenStackDeployOptions returns default deploy options for the OpenStack infra provider.
func NewOpenStackDeployOptions() *OpenStackDeployOptions {
	return &OpenStackDeployOptions{
		CloudName:                 "openstack",
		NodeCIDR:                  "10.6.0.0/24",
		ControlPlaneMachineFlavor: "m1.large",
		NodeMachineFlavor:         "m1.large",
		CloudProviderVersion:      "v1.30.0",
		CalicoVersion:             "v3.24.1",
	}
}

// Configure implements Provider interface.
func (s *OpenStackProvider) Configure(providerOptions any) error {
	opts, ok := providerOptions.(*OpenStackSetupOptions)
	if !ok {
		return fmt.Errorf("expected OpenStackSetupOptions as the first argument")
	}

	s.CloudYAMLB64 = opts.CloudYAML
	s.CACertB64 = opts.CACert

	return nil
}

// Name implements Provider interface.
func (s *OpenStackProvider) Name() string {
	return constants.OpenStackProviderName
}

// Namespace implements Provider interface.
func (s *OpenStackProvider) Namespace() string {
	return s.ProviderNS
}

// WatchingNamespace implements Provider interface.
func (s *OpenStackProvider) WatchingNamespace() string {
	return s.WatchingNS
}

// Version implements Provider interface.
func (s *OpenStackProvider) Version() string {
	return s.ProviderVersion
}

// ProviderVars returns config overrides for the provider installation.
func (s *OpenStackProvider) ProviderVars() (Variables, error) {
	vars := make(Variables)
	vars["OPENSTACK_CLOUD_YAML_B64"] = s.CloudYAMLB64
	vars["OPENSTACK_CLOUD_CACERT_B64"] = s.CACertB64

	return vars, nil
}

// IsInstalled implements Provider interface.
func (s *OpenStackProvider) IsInstalled(ctx context.Context, clientset *kubernetes.Clientset) (bool, error) {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, s.Namespace(), metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	if _, err := clientset.AppsV1().Deployments(s.Namespace()).Get(ctx, "capo-controller-manager", metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// ClusterVars returns config overrides for template generation.
func (s *OpenStackProvider) ClusterVars(opts any) (Variables, error) {
	var (
		deployOptions = NewOpenStackDeployOptions()
		ok            bool
	)

	if opts != nil {
		deployOptions, ok = opts.(*OpenStackDeployOptions)
		if !ok {
			return nil, fmt.Errorf("OpenStack deployment provider expects OpenStackDeployOptions as the deployment options")
		}
	}

	vars := Variables{
		"OPENSTACK_CLOUD_YAML_B64":               s.CloudYAMLB64,
		"OPENSTACK_CLOUD_CACERT_B64":             s.CACertB64,
		"OPENSTACK_CLOUD":                        deployOptions.CloudName,
		"OPENSTACK_EXTERNAL_NETWORK_ID":          deployOptions.ExternalNetworkID,
		"OPENSTACK_IMAGE_NAME":                   deployOptions.ImageName,
		"OPENSTACK_FAILURE_DOMAIN":               deployOptions.FailureDomain,
		"OPENSTACK_DNS_NAMESERVERS":              deployOptions.DNSNameservers,
		"OPENSTACK_NODE_CIDR":                    deployOptions.NodeCIDR,
		"OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR": deployOptions.ControlPlaneMachineFlavor,
		"OPENSTACK_NODE_MACHINE_FLAVOR":          deployOptions.NodeMachineFlavor,
		"OPENSTACK_CLOUD_PROVIDER_VERSION":       deployOptions.CloudProviderVersion,
		"CALICO_VERSION":                         deployOptions.CalicoVersion,
	}

	return vars, nil
}

// GetClusterTemplate implements Provider interface.
func (s *OpenStackProvider) GetClusterTemplate(client client.Client, opts client.GetClusterTemplateOptions) (client.Template, error) {
	return client.GetClusterTemplate(context.TODO(), opts)
}

// WaitReady implements Provider interface.
func (s *OpenStackProvider) WaitReady(ctx context.Context, clientset *kubernetes.Clientset) error {
	return retry.Constant(10*time.Minute, retry.WithUnits(10*time.Second), retry.WithErrorLogging(true)).Retry(func() error {
		if _, err := clientset.CoreV1().Namespaces().Get(ctx, s.Namespace(), metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		var (
			err        error
			deployment *v1.Deployment
		)
		if deployment, err = clientset.AppsV1().Deployments(s.Namespace()).Get(ctx, "capo-controller-manager", metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		if deployment.Status.ReadyReplicas != deployment.Status.Replicas || deployment.Status.ReadyReplicas == 0 {
			return retry.ExpectedError(fmt.Errorf("%d of %d replicas ready", deployment.Status.ReadyReplicas, deployment.Status.Replicas))
		}

		return nil
	})
}
