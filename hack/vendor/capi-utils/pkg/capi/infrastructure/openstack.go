// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package infrastructure

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/siderolabs/go-retry/retry"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"

	"github.com/siderolabs/capi-utils/pkg/constants"
)

// rootVolumeChildIndent/rootVolumeGrandchildIndent are the absolute
// indentation levels (in spaces) required for lines nested under the
// generated "rootVolume:" block once it is spliced into
// openstack-standard.yaml's OpenStackMachineTemplate.spec.template.spec,
// where "rootVolume:" itself lands at 6 spaces (a sibling of "flavor:" /
// "identityRef:"). envsubst performs plain text substitution, so
// continuation lines must carry their own indentation - the template can
// only supply indentation for the first line.
const (
	rootVolumeChildIndent      = "        "   // 8 spaces: sizeGiB/type/availabilityZone
	rootVolumeGrandchildIndent = "          " // 10 spaces: availabilityZone.name
)

// renderRootVolume returns the YAML fragment for OpenStackMachineTemplate's
// optional "rootVolume" field, driven by a Cinder volume type (backend) name
// and size. When sizeGiB is not positive, no rootVolume is requested, and a
// harmless YAML comment is returned instead so the machine keeps using
// ephemeral (local, hypervisor-backed) storage - CAPO's default when
// rootVolume is omitted entirely.
func renderRootVolume(sizeGiB int, volumeType, availabilityZone string) string {
	if sizeGiB <= 0 {
		return "# ephemeral storage (no rootVolume configured)"
	}

	lines := []string{
		"rootVolume:",
		fmt.Sprintf("%ssizeGiB: %d", rootVolumeChildIndent, sizeGiB),
	}

	if volumeType != "" {
		lines = append(lines, fmt.Sprintf("%stype: %s", rootVolumeChildIndent, volumeType))
	}

	if availabilityZone != "" {
		lines = append(lines,
			fmt.Sprintf("%savailabilityZone:", rootVolumeChildIndent),
			fmt.Sprintf("%sname: %s", rootVolumeGrandchildIndent, availabilityZone),
		)
	}

	return strings.Join(lines, "\n")
}

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

	// ControlPlaneVolumeType/NodeVolumeType name a Cinder volume type
	// (backend) to boot control plane/worker machines from a Cinder-backed
	// root volume instead of the hypervisor's local ephemeral storage. Only
	// used when the matching *SizeGiB field is > 0; when empty, CAPO falls
	// back to the cloud's default Cinder volume type.
	ControlPlaneVolumeType string
	NodeVolumeType         string

	// ControlPlaneVolumeSizeGiB/NodeVolumeSizeGiB is the size, in GiB, of
	// the Cinder root volume for control plane/worker machines. Leaving
	// this at 0 (the default) keeps machines on ephemeral storage - no
	// rootVolume is requested at all.
	ControlPlaneVolumeSizeGiB int
	NodeVolumeSizeGiB         int

	// ControlPlaneVolumeAvailabilityZone/NodeVolumeAvailabilityZone
	// optionally pins the Cinder root volume to a specific availability
	// zone. Only meaningful alongside a positive *VolumeSizeGiB.
	ControlPlaneVolumeAvailabilityZone string
	NodeVolumeAvailabilityZone         string
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
		"OPENSTACK_CONTROL_PLANE_ROOT_VOLUME": renderRootVolume(
			deployOptions.ControlPlaneVolumeSizeGiB,
			deployOptions.ControlPlaneVolumeType,
			deployOptions.ControlPlaneVolumeAvailabilityZone,
		),
		"OPENSTACK_NODE_ROOT_VOLUME": renderRootVolume(
			deployOptions.NodeVolumeSizeGiB,
			deployOptions.NodeVolumeType,
			deployOptions.NodeVolumeAvailabilityZone,
		),
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
