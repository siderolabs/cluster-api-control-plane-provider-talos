// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/siderolabs/capi-utils/pkg/capi"
	"github.com/siderolabs/capi-utils/pkg/capi/infrastructure"
	"github.com/siderolabs/capi-utils/pkg/constants"
)

var clusterCreateCmdFlags struct {
	templatePath string
}

var deployOptions = capi.DefaultDeployOptions()

var awsDeployOptions = infrastructure.NewAWSDeployOptions()

var clusterCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Deploy a cluster using CAPI.",
	Long:  ``,
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()

		opts := []capi.DeployOption{
			capi.WithDeployOptions(deployOptions),
			capi.WithClusterNamespace(clusterCmdFlags.clusterNamespace),
		}

		if deployOptions.Provider == "" || deployOptions.Provider == constants.AWSProviderName {
			opts = append(
				opts,
				capi.WithProviderOptions(awsDeployOptions),
			)
		}

		if clusterCreateCmdFlags.templatePath != "" {
			opts = append(opts, capi.WithTemplateFile(clusterCreateCmdFlags.templatePath))
		}

		cluster, err := manager.DeployCluster(ctx, clusterCmdFlags.clusterName, opts...)
		if err != nil {
			return err
		}

		return cluster.Health(ctx)
	},
}

func init() {
	clusterCmd.AddCommand(clusterCreateCmd)

	clusterCreateCmd.Flags().StringVarP(&clusterCreateCmdFlags.templatePath, "from", "f",
		"https://github.com/siderolabs/cluster-api-templates/blob/main/aws/standard/standard.yaml", "Custom path for the cluster template")
	clusterCreateCmd.Flags().Int64Var(&deployOptions.ControlPlaneNodes, "control-plane-nodes", deployOptions.ControlPlaneNodes, "Number of control plane nodes to deploy")
	clusterCreateCmd.Flags().Int64Var(&deployOptions.WorkerNodes, "worker-nodes", deployOptions.WorkerNodes, "Number of worker nodes to deploy")
	clusterCreateCmd.Flags().StringVarP(&deployOptions.Provider, "provider", "p", deployOptions.Provider, "Infrastructure provider to use for the deployment")
	clusterCreateCmd.Flags().StringVar(&deployOptions.ProviderVersion, "provider-version", deployOptions.ProviderVersion, "Provider version to use")
	clusterCreateCmd.Flags().StringVar(&deployOptions.KubernetesVersion, "kubernetes-version", deployOptions.KubernetesVersion, "Kubernetes version to use")
	clusterCreateCmd.Flags().StringVar(&deployOptions.TalosVersion, "talos-version", deployOptions.TalosVersion, "Talos version to use")
	// AWS provider flags
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.CloudProviderVersion, "aws-cloud-provider-version", awsDeployOptions.CloudProviderVersion, "AWS cloud provider version")

	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.ControlPlaneAMIID, "aws-cp-ami-id", awsDeployOptions.ControlPlaneAMIID, "AWS AMI ID for control plane nodes")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.ControlPlaneADDLSecGroups, "aws-cp-addl-sec-groups", awsDeployOptions.ControlPlaneADDLSecGroups, "AWS control plane ADDL sec groups")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.ControlPlaneIAMProfile, "aws-cp-iam-profile", awsDeployOptions.ControlPlaneIAMProfile, "AWS control plane IAM profile")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.ControlPlaneMachineType, "aws-cp-machine-type", awsDeployOptions.ControlPlaneMachineType, "AWS control plane machine type")
	clusterCreateCmd.Flags().Int64Var(&awsDeployOptions.ControlPlaneVolSize, "aws-cp-vol-size", awsDeployOptions.ControlPlaneVolSize, "AWS control plane vol size")

	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.NodeAMIID, "aws-worker-ami-id", awsDeployOptions.NodeAMIID, "AWS AMI ID for worker nodes")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.NodeADDLSecGroups, "aws-worker-addl-sec-groups", awsDeployOptions.NodeADDLSecGroups, "AWS worker ADDL sec groups")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.NodeIAMProfile, "aws-worker-iam-profile", awsDeployOptions.NodeIAMProfile, "AWS worker IAM profile")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.NodeMachineType, "aws-worker-machine-type", awsDeployOptions.NodeMachineType, "AWS worker machine type")
	clusterCreateCmd.Flags().Int64Var(&awsDeployOptions.NodeVolSize, "aws-worker-vol-size", awsDeployOptions.NodeVolSize, "AWS worker vol size")

	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.Region, "aws-region", awsDeployOptions.Region, "AWS region")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.Subnet, "aws-subnet", awsDeployOptions.NodeADDLSecGroups, "AWS subnet")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.SSHKeyName, "aws-ssh-key-name", awsDeployOptions.SSHKeyName, "AWS ssh key name")
	clusterCreateCmd.Flags().StringVar(&awsDeployOptions.VPCID, "aws-vpc-id", awsDeployOptions.VPCID, "AWS VPC ID")
}
