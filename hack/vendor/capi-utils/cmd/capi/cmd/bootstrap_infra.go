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

var (
	targetNS   string
	watchingNS string
)

var capiInfraCmd = &cobra.Command{
	Use:   "infra",
	Short: "Install and patch CAPI infra providers.",
	Long:  ``,
	Example: `
	## Specifying multiple infra providers
	capi bootstrap infra --providers aws,sidero

	## Specifying namespaces and version for infra provider
	capi bootstrap infra --providers aws:v0.6.8 --target-ns my-ns --watching-ns my-ns-to-watch
	`,
	RunE: func(*cobra.Command, []string) error {
		ctx := context.Background()

		providers := make([]infrastructure.Provider, len(options.InfrastructureProviders))
		for i, name := range options.InfrastructureProviders {
			provider, err := infrastructure.NewProvider(
				name,
				infrastructure.WithProviderNS(targetNS),
				infrastructure.WithWatchingNS(watchingNS),
			)
			if err != nil {
				return err
			}

			if opts, ok := setupOptions[provider.Name()]; ok {
				if err = provider.Configure(opts); err != nil {
					return err
				}
			}

			providers[i] = provider
		}

		var err error

		manager, err = capi.NewManager(ctx, capi.Options{
			ClusterctlConfigPath:    options.ClusterctlConfigPath,
			CoreProvider:            "",
			BootstrapProviders:      []string{},
			InfrastructureProviders: providers,
			ControlPlaneProviders:   []string{},
		})
		if err != nil {
			return err
		}

		return manager.Install(ctx)
	},
}

func init() {
	bootstrapCmd.AddCommand(capiInfraCmd)
	capiInfraCmd.PersistentFlags().StringSliceVar(&options.InfrastructureProviders, "providers", []string{"aws"}, "Name(s) of infra provider(s) to init")
	capiInfraCmd.PersistentFlags().StringVar(&targetNS, "target-ns", "", "Namespace to install proivder in")
	capiInfraCmd.PersistentFlags().StringVar(&watchingNS, "watching-ns", "", "Namespace for provider to watch")

	// AWS provider flags
	opts := infrastructure.NewAWSSetupOptions()
	capiInfraCmd.PersistentFlags().StringVar(&opts.AWSCredentials, "aws-base64-encoded-credentials", awsOptions.b64EncodedCredentials, "AWS_B64ENCODED_CREDENTIALS")
	setupOptions[constants.AWSProviderName] = opts
}
