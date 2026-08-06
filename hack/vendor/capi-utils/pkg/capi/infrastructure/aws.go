// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package infrastructure

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/siderolabs/go-retry/retry"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"

	"github.com/siderolabs/capi-utils/pkg/constants"
)

// NewAWSProvider creates new AWS infrastructure provider.
func NewAWSProvider(version, providerNS, watchingNS string) (*AWSProvider, error) {
	if providerNS == "" {
		providerNS = constants.AWSCAPANamespace
	}

	return &AWSProvider{
		ProviderVersion: version,
		ProviderNS:      providerNS,
		WatchingNS:      watchingNS,
	}, nil
}

// AWSProvider infrastructure provider.
type AWSProvider struct {
	B64EncodedCredentials string
	ProviderVersion       string
	ProviderNS            string
	WatchingNS            string
}

// NewAWSSetupOptions creates new AWSSetupOptions.
func NewAWSSetupOptions() *AWSSetupOptions {
	return &AWSSetupOptions{}
}

// AWSSetupOptions AWS specific setup options.
type AWSSetupOptions struct {
	AWSCredentials       string
	AWSProviderNamespace string
	AWSWatchingNamespace string
}

// AWSDeployOptions defines provider specific settings for cluster deployment.
type AWSDeployOptions struct {
	ControlPlaneMachineType   string
	ControlPlaneIAMProfile    string
	ControlPlaneAMIID         string
	ControlPlaneADDLSecGroups string
	NodeMachineType           string
	NodeIAMProfile            string
	NodeAMIID                 string
	NodeADDLSecGroups         string
	Region                    string
	SSHKeyName                string
	VPCID                     string
	Subnet                    string
	CloudProviderVersion      string
	CalicoVersion             string
	ControlPlaneVolSize       int64
	NodeVolSize               int64
}

// NewAWSDeployOptions returns default deploy options for the AWS infra provider.
func NewAWSDeployOptions() *AWSDeployOptions {
	return &AWSDeployOptions{
		ControlPlaneVolSize:     50,
		NodeVolSize:             50,
		ControlPlaneMachineType: "t3.large",
		ControlPlaneIAMProfile:  "CAPI_AWS_ControlPlane",
		NodeMachineType:         "t3.large",
		NodeIAMProfile:          "CAPI_AWS_Worker",
		CloudProviderVersion:    "v1.22.3",
		CalicoVersion:           "v3.24.1",
	}
}

// Configure implements Provider interface.
func (s *AWSProvider) Configure(providerOptions any) error {
	opts, ok := providerOptions.(*AWSSetupOptions)
	if !ok {
		return fmt.Errorf("expected AWSSetupOptions as the first argument")
	}

	s.B64EncodedCredentials = opts.AWSCredentials

	return nil
}

// Name implements Provider interface.
func (s *AWSProvider) Name() string {
	return constants.AWSProviderName
}

// Namespace implements Provider interface.
func (s *AWSProvider) Namespace() string {
	return s.ProviderNS
}

// WatchingNamespace implements Provider interface.
func (s *AWSProvider) WatchingNamespace() string {
	return s.WatchingNS
}

// Version implements Provider interface.
func (s *AWSProvider) Version() string {
	return s.ProviderVersion
}

// ProviderVars returns config overrides for the provider installation.
func (s *AWSProvider) ProviderVars() (Variables, error) {
	vars := make(Variables)
	vars["AWS_B64ENCODED_CREDENTIALS"] = s.B64EncodedCredentials

	return vars, nil
}

// IsInstalled implements Provider interface.
func (s *AWSProvider) IsInstalled(ctx context.Context, clientset *kubernetes.Clientset) (bool, error) {
	_, err := clientset.CoreV1().Namespaces().Get(ctx, s.Namespace(), metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	if _, err := clientset.AppsV1().Deployments(s.Namespace()).Get(ctx, "capa-controller-master", metav1.GetOptions{}); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// ClusterVars returns config overrides for template generation.
func (s *AWSProvider) ClusterVars(opts any) (Variables, error) {
	var (
		deployOptions = NewAWSDeployOptions()
		ok            bool
	)

	if opts != nil {
		deployOptions, ok = opts.(*AWSDeployOptions)
		if !ok {
			return nil, fmt.Errorf("AWS deployment provider expects aws.DeployOptions as the deployment options")
		}
	}

	vars := Variables{
		"AWS_REGION":                        deployOptions.Region,
		"AWS_VPC_ID":                        deployOptions.VPCID,
		"AWS_SUBNET":                        deployOptions.Subnet,
		"AWS_SSH_KEY_NAME":                  deployOptions.SSHKeyName,
		"AWS_CONTROL_PLANE_MACHINE_TYPE":    deployOptions.ControlPlaneMachineType,
		"AWS_CONTROL_PLANE_VOL_SIZE":        strconv.FormatInt(deployOptions.ControlPlaneVolSize, 10),
		"AWS_CONTROL_PLANE_ADDL_SEC_GROUPS": deployOptions.ControlPlaneADDLSecGroups,
		"AWS_CONTROL_PLANE_IAM_PROFILE":     deployOptions.ControlPlaneIAMProfile,
		"AWS_CONTROL_PLANE_AMI_ID":          deployOptions.ControlPlaneAMIID,
		"AWS_NODE_MACHINE_TYPE":             deployOptions.NodeMachineType,
		"AWS_NODE_VOL_SIZE":                 strconv.FormatInt(deployOptions.NodeVolSize, 10),
		"AWS_NODE_ADDL_SEC_GROUPS":          deployOptions.NodeADDLSecGroups,
		"AWS_NODE_IAM_PROFILE":              deployOptions.NodeIAMProfile,
		"AWS_NODE_AMI_ID":                   deployOptions.NodeAMIID,
		"AWS_CLOUD_PROVIDER_VERSION":        deployOptions.CloudProviderVersion,
		"CALICO_VERSION":                    deployOptions.CalicoVersion,
	}

	return vars, nil
}

// GetClusterTemplate implements Provider interface.
func (s *AWSProvider) GetClusterTemplate(client client.Client, opts client.GetClusterTemplateOptions) (client.Template, error) {
	return client.GetClusterTemplate(context.TODO(), opts)
}

// WaitReady implements Provider interface.
func (s *AWSProvider) WaitReady(ctx context.Context, clientset *kubernetes.Clientset) error {
	return retry.Constant(10*time.Minute, retry.WithUnits(10*time.Second), retry.WithErrorLogging(true)).Retry(func() error {
		if _, err := clientset.CoreV1().Namespaces().Get(ctx, s.Namespace(), metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		var (
			err        error
			deployment *v1.Deployment
		)
		if deployment, err = clientset.AppsV1().Deployments(s.Namespace()).Get(ctx, "capa-controller-manager", metav1.GetOptions{}); err != nil {
			return retry.ExpectedError(err)
		}

		if deployment.Status.ReadyReplicas != deployment.Status.Replicas || deployment.Status.ReadyReplicas == 0 {
			return retry.ExpectedError(fmt.Errorf("%d of %d replicas ready", deployment.Status.ReadyReplicas, deployment.Status.Replicas))
		}

		return nil
	})
}
