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
	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	machineapi "github.com/talos-systems/talos/pkg/machinery/api/machine"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	logf "sigs.k8s.io/cluster-api/cmd/clusterctl/log"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/talos-systems/capi-utils/pkg/capi"
	"github.com/talos-systems/capi-utils/pkg/capi/infrastructure"
	"github.com/talos-systems/go-retry/retry"
)

var (
	talosVersion *semver.Version
	scaleVersion *semver.Version
)

type clusterctlConfig struct {
	Providers []providerConfig `yaml:"providers"`
}

type providerConfig struct {
	Name         string                 `yaml:"name"`
	Url          string                 `yaml:"url"`
	ProviderType clusterv1.ProviderType `yaml:"type"`
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
	verbosity := 6
	logf.SetLogger(logf.NewLogger(logf.WithThreshold(&verbosity)))

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

	var (
		clusterctlConfigPath string
		clusterctlConfigs    *clusterctlConfig = &clusterctlConfig{}
	)

	for _, config := range []struct {
		env          string
		providerType clusterv1.ProviderType
	}{
		{
			env:          "CONTROL_PLANE_PROVIDER_COMPONENTS",
			providerType: clusterv1.ControlPlaneProviderType,
		},
		{
			env:          "BOOTSTRAP_PROVIDER_COMPONENTS",
			providerType: clusterv1.BootstrapProviderType,
		},
	} {
		customConfig := os.Getenv(config.env)

		if customConfig != "" {
			if clusterctlConfigs.Providers == nil {
				clusterctlConfigs.Providers = []providerConfig{}
			}

			clusterctlConfigs.Providers = append(clusterctlConfigs.Providers, providerConfig{
				Name:         "talos",
				Url:          fmt.Sprintf("file://%s", customConfig),
				ProviderType: config.providerType,
			})
		}
	}

	if clusterctlConfigs.Providers != nil {
		config, err := os.CreateTemp("", "clusterctlConfig*.yaml")
		suite.Require().NoError(err)
		defer os.Remove(config.Name())

		clusterctlConfigPath = config.Name()

		encoder := yaml.NewEncoder(config)
		suite.Require().NoError(encoder.Encode(clusterctlConfigs))
		suite.Require().NoError(encoder.Close())
	}

	options := capi.Options{
		CoreProvider:            env("CORE_PROVIDER", "cluster-api:v0.4.3"),
		BootstrapProviders:      []string{"talos:v0.4.0-alpha.0"}, //TODO: remove version pinning when 0.4.0 is out
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

	time.Sleep(time.Second * 5)

	id := uuid.New()

	cluster, err := manager.DeployCluster(suite.ctx, fmt.Sprintf("caccpt-test-cluster-%s", id.String()[:7]),
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

// Test01ScaleUp scale control plane nodes up.
func (suite *IntegrationSuite) Test01ScaleUp() {
	suite.cluster.Scale(suite.ctx, 3, capi.ControlPlaneNodes) //nolint:errcheck

	suite.Require().NoError(suite.cluster.Health(suite.ctx))

	time.Sleep(2 * time.Second)
}

// Test02ReconcileMachine remove a machine and wait until cluster gets healthy again.
func (suite *IntegrationSuite) Test02ReconcileMachine() {
	selector, err := labels.Parse("cluster.x-k8s.io/control-plane")
	suite.Require().NoError(err)

	client, err := suite.manager.GetClient(suite.ctx)
	suite.Require().NoError(err)

	machines := unstructured.UnstructuredList{}
	machines.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Kind:    "Machine",
		Version: suite.manager.Version(),
	})

	suite.Require().NoError(client.List(suite.ctx, &machines, &runtimeclient.MatchingLabelsSelector{
		Selector: selector,
	}))

	getEtcdMembers := func() (map[uint64]struct{}, error) {
		members := map[uint64]struct{}{}

		talosclient, e := suite.cluster.TalosClient(suite.ctx)
		if e != nil {
			return nil, e
		}

		resp, e := talosclient.EtcdMemberList(suite.ctx, &machineapi.EtcdMemberListRequest{})
		if e != nil {
			return nil, e
		}

		for _, m := range resp.Messages[0].Members {
			members[m.Id] = struct{}{}
		}

		return members, nil
	}

	oldEtcdMembers, err := getEtcdMembers()
	suite.Require().NoError(err)

	suite.Require().Greater(len(machines.Items), 0)

	machine := machines.Items[0]

	suite.Require().NoError(client.Delete(suite.ctx, &machine))

	suite.Require().NoError(
		retry.Constant(15*time.Minute, retry.WithUnits(4*time.Second), retry.WithErrorLogging(true)).Retry(func() error {
			if e := suite.cluster.Sync(suite.ctx); e != nil {
				return retry.ExpectedError(e)
			}

			etcdMembers, e := getEtcdMembers()
			if e != nil {
				return retry.ExpectedError(e)
			}

			if len(etcdMembers) != len(oldEtcdMembers) {
				return retry.ExpectedErrorf("expected %d etcd members, got %d", len(oldEtcdMembers), len(etcdMembers))
			}

			membersDiffer := false
			for m := range etcdMembers {
				if _, ok := oldEtcdMembers[m]; !ok {
					membersDiffer = true

					break
				}
			}

			if !membersDiffer {
				return retry.ExpectedErrorf("no etcd members change detected")
			}

			if e := suite.manager.CheckClusterReady(suite.ctx, suite.cluster); e != nil {
				return retry.ExpectedError(e)
			}

			return nil
		}),
	)
}

// Test03ScaleDown scale control planes down.
func (suite *IntegrationSuite) Test03ScaleDown() {
	suite.Require().NoError(suite.cluster.Sync(suite.ctx))

	err := suite.cluster.Scale(suite.ctx, 1, capi.ControlPlaneNodes)
	suite.Require().NoError(err)

	time.Sleep(2 * time.Second)

	suite.Require().NoError(suite.cluster.Sync(suite.ctx))
}

// Test04ScaleControlPlaneNoWait scale control plane nodes up and down without waiting.
func (suite *IntegrationSuite) Test04ScaleControlPlaneNoWait() {
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
