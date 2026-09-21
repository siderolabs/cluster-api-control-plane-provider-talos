// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package cmd

// Options control the sidero testing.
type Options struct {
	ClusterctlConfigPath string
	CoreProvider         string

	BootstrapProviders      []string
	InfrastructureProviders []string
	ControlPlaneProviders   []string
}

// DefaultOptions returns default settings.
func DefaultOptions() Options {
	return Options{
		CoreProvider:            "cluster-api:v1.1.0",
		BootstrapProviders:      []string{"talos"},
		InfrastructureProviders: []string{"aws"},
		ControlPlaneProviders:   []string{"talos"},
	}
}
