// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/siderolabs/capi-utils/pkg/capi"
	"github.com/siderolabs/capi-utils/pkg/capi/infrastructure"
)

var capiCoreCmd = &cobra.Command{
	Use:   "core",
	Short: "Install and patch core CAPI.",
	Long: `
	This command installs everything for CAPI *but* infrastructure providers.
	It is assumed that these core components are global watchers and can handle
	reconciling CAPI resources across any namespace.
	`,
	Example: `
	capi bootstrap core
	`,
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()

		var err error

		manager, err = capi.NewManager(ctx, capi.Options{
			ClusterctlConfigPath:    options.ClusterctlConfigPath,
			CoreProvider:            options.CoreProvider,
			BootstrapProviders:      options.BootstrapProviders,
			InfrastructureProviders: []infrastructure.Provider{},
			ControlPlaneProviders:   options.ControlPlaneProviders,
		})
		if err != nil {
			return err
		}

		return manager.Install(ctx)
	},
}

func init() {
	bootstrapCmd.AddCommand(capiCoreCmd)
}
