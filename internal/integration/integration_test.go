// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package integration_test contains core runners for integration tests
package integration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/stretchr/testify/suite"
	"gopkg.in/yaml.v3"
	capiv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"

	"github.com/talos-systems/capi-utils/pkg/capi"
	"github.com/talos-systems/capi-utils/pkg/capi/infrastructure"
)

var (
	talosVersion *semver.Version
	scaleVersion *semver.Version
)

type clusterctlConfig struct {
	Providers []controlplaneProvider `yaml:"providers"`
}

type controlplaneProvider struct {
	Name         string              `yaml:"name"`
	Url          string              `yaml:"url"`
	ProviderType capiv1.ProviderType `yaml:"type"`
}

type IntegrationSuite struct {
	suite.Suite

	manager *capi.Manager
	cluster *capi.Cluster
	ctx     context.Context
	cancel  context.CancelFunc
}

func (suite *IntegrationSuite) SetupSuite() {
	suite.ctx, suite.cancel = context.WithTimeout(context.Background(), 1*time.Hour)

	env := func(key, def string) string {
		val := os.Getenv(key)
		if val != "" {
			return val
		}

		return def
	}

	providerType := env("PROVIDER", "aws:v0.6.7")

	provider, err := infrastructure.NewProvider(providerType)
	suite.Require().NoError(err)

	var clusterctlConfigPath string

	customInfra := os.Getenv("INFRASTRUCTURE_COMPONENTS_PATH")
	if customInfra != "" {
		config, err := os.CreateTemp("", "clusterctlConfig*.yaml")
		suite.Require().NoError(err)
		defer os.Remove(config.Name())

		clusterctlConfigPath = config.Name()

		encoder := yaml.NewEncoder(config)
		suite.Require().NoError(encoder.Encode(&clusterctlConfig{
			Providers: []controlplaneProvider{
				{
					Name:         "talos",
					Url:          fmt.Sprintf("file://%s", customInfra),
					ProviderType: capiv1.ControlPlaneProviderType,
				},
			},
		}))
		suite.Require().NoError(encoder.Close())
	}

	options := capi.Options{
		CoreProvider:            env("CORE_PROVIDER", "cluster-api:v0.3.19"),
		BootstrapProviders:      []string{"talos"},
		InfrastructureProviders: []infrastructure.Provider{provider},
		ControlPlaneProviders:   []string{"talos"},
	}

	if clusterctlConfigPath != "" {
		options.ClusterctlConfigPath = clusterctlConfigPath
	}

	manager, err := capi.NewManager(suite.ctx, options)
	suite.Require().NoError(err)

	suite.manager = manager

	err = manager.Install(suite.ctx)
	suite.Require().NoError(err)

	cluster, err := manager.DeployCluster(suite.ctx, "caccpt-test-cluster",
		capi.WithProvider(provider.Name()),
		capi.WithKubernetesVersion(strings.TrimLeft(env("WORKLOAD_KUBERNETES_VERSION", env("K8S_VERSION", "v1.21.3")), "v")),
		capi.WithTemplate(infrastructure.AWSTalosTemplate),
	)
	suite.Require().NoError(err)

	suite.cluster = cluster

	suite.Require().NoError(suite.cluster.Health(suite.ctx))
}

func (suite *IntegrationSuite) TearDownSuite() {
	suite.cancel()

	if suite.cluster != nil {
		err := suite.manager.DestroyCluster(context.Background(), suite.cluster.Name(), suite.cluster.Namespace())
		suite.Require().NoError(err)
	}
}

// TestScaleControlPlane scale control plane nodes.
func (suite *IntegrationSuite) TestScaleControlPlane() {
	err := suite.cluster.Scale(suite.ctx, 3, capi.ControlPlaneNodes)
	suite.Require().NoError(err)

	time.Sleep(2 * time.Second)

	suite.Require().NoError(suite.cluster.Sync(suite.ctx))

	if talosVersion.LessThan(*scaleVersion) {
		suite.T().Skip("skip for Talos <= v0.12.2, scale down is unstable")
	}

	err = suite.cluster.Scale(suite.ctx, 1, capi.ControlPlaneNodes)
	suite.Require().NoError(err)

	time.Sleep(2 * time.Second)

	suite.Require().NoError(suite.cluster.Sync(suite.ctx))
}

// TestScaleControlPlaneNoWait scale control plane nodes.
func (suite *IntegrationSuite) TestScaleControlPlaneNoWait() {
	if talosVersion.LessThan(*scaleVersion) {
		suite.T().Skip("skip for Talos <= v0.12.2, scale down is unstable")
	}

	ctx, cancel := context.WithCancel(suite.ctx)
	go func() {
		time.Sleep(time.Second * 5)

		cancel()
	}()

	suite.cluster.Scale(ctx, 3, capi.ControlPlaneNodes) //nolint:errcheck

	err := suite.cluster.Scale(suite.ctx, 1, capi.ControlPlaneNodes)
	suite.Require().NoError(err)
}

// TestIntegration runs integration tests.
func TestIntegration(t *testing.T) {
	version := os.Getenv("WORKLOAD_TALOS_VERSION")

	var err error

	talosVersion, err = semver.NewVersion(strings.TrimLeft(version, "v"))
	if err != nil {
		t.Fatalf("failed to get talos version %s", err)
	}

	scaleVersion, _ = semver.NewVersion("0.12.2") //nolint:errcheck

	suite.Run(t, new(IntegrationSuite))
}
