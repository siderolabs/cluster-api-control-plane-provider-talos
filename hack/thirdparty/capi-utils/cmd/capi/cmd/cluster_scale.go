// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/siderolabs/capi-utils/pkg/capi"
)

var clusterScaleCmdFlags struct {
	group    string
	replicas int
}

var groups = map[string]capi.NodeGroup{
	"control-planes": capi.ControlPlaneNodes,
	"workers":        capi.WorkerNodes,
}

var clusterScaleCmd = &cobra.Command{
	Use:   "scale",
	Short: "Scale a CAPI cluster.",
	Long:  ``,
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()

		group, ok := groups[clusterScaleCmdFlags.group]
		if !ok {
			return fmt.Errorf("nodes can be either 'control-planes' or 'workers', got: %q", clusterScaleCmdFlags.group)
		}

		if clusterScaleCmdFlags.replicas < 0 {
			return fmt.Errorf("number of replicas is required")
		}

		cluster, err := manager.NewCluster(ctx, clusterCmdFlags.clusterName, clusterCmdFlags.clusterNamespace)
		if err != nil {
			return err
		}

		return cluster.Scale(ctx, clusterScaleCmdFlags.replicas, group)
	},
}

func init() {
	clusterCmd.AddCommand(clusterScaleCmd)

	clusterScaleCmd.Flags().IntVarP(&clusterScaleCmdFlags.replicas, "replicas", "r", -1, "Desired replicas count")
	clusterScaleCmd.Flags().StringVarP(
		&clusterScaleCmdFlags.group,
		"nodes", "",
		"control-planes",
		"Nodes to scale; valid values are 'control-planes' or 'workers'",
	)
}
