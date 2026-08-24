// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/siderolabs/capi-utils/pkg/capi"
)

var clusterCmdFlags struct {
	clusterName      string
	clusterNamespace string
}

var clusterCmd = &cobra.Command{
	Use: "cluster",
	PersistentPreRunE: func(*cobra.Command, []string) error {
		ctx := context.Background()

		var err error

		manager, err = capi.NewManager(ctx, capi.Options{})
		if err != nil {
			return err
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(clusterCmd)

	clusterCmd.PersistentFlags().StringVarP(&clusterCmdFlags.clusterName, "name", "n", "talos-default", "CAPI cluster name")
	clusterCmd.PersistentFlags().StringVarP(&clusterCmdFlags.clusterNamespace, "namespace", "N", "default", "CAPI cluster namespace")
}
