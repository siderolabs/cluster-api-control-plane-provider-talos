// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package infrastructure contains infrastructure providers setup hooks and ready conditions.
package infrastructure

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"

	"github.com/siderolabs/capi-utils/pkg/constants"
)

// Variables is a map of key value pairs of config parameters.
type Variables map[string]string

// Provider defines an interface for the infrastructure provider.
type Provider interface {
	Name() string
	Namespace() string
	Version() string
	WatchingNamespace() string
	Configure(any) error
	ProviderVars() (Variables, error)
	ClusterVars(any) (Variables, error)
	IsInstalled(ctx context.Context, clientset *kubernetes.Clientset) (bool, error)
	GetClusterTemplate(client.Client, client.GetClusterTemplateOptions) (client.Template, error)
	WaitReady(context.Context, *kubernetes.Clientset) error
}

// ProviderOptions is the functional options struct.
type ProviderOptions struct {
	ProviderNS string
	WatchingNS string
}

// ProviderOption is the functional options func.
type ProviderOption func(*ProviderOptions)

// WithProviderNS sets the namespace to something non-default.
func WithProviderNS(ns string) ProviderOption {
	return func(opts *ProviderOptions) {
		opts.ProviderNS = ns
	}
}

// WithWatchingNS sets the watching namespace to something non-global.
func WithWatchingNS(ns string) ProviderOption {
	return func(opts *ProviderOptions) {
		opts.WatchingNS = ns
	}
}

// NewProvider creates a new provider from a specified type.
func NewProvider(providerType string, opts ...ProviderOption) (Provider, error) {
	// Handle any functional options
	providerOpts := &ProviderOptions{}

	for _, opt := range opts {
		opt(providerOpts)
	}

	// Parse out version from provider string
	var version string

	parts := strings.Split(providerType, ":")

	if len(parts) > 1 {
		version = parts[1]
	}

	if parts[0] == constants.AWSProviderName {
		return NewAWSProvider(
			version,
			providerOpts.ProviderNS,
			providerOpts.WatchingNS,
		)
	}

	return nil, fmt.Errorf("unknown infrastructure provider type %s", parts[0])
}
