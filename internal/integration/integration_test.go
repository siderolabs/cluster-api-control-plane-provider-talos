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
	"github.com/siderolabs/capi-utils/pkg/capi"
	"github.com/siderolabs/capi-utils/pkg/capi/infrastructure"
	"github.com/stretchr/testify/suite"
	"github.com/talos-systems/go-retry/retry"
	machineapi "github.com/talos-systems/talos/pkg/machinery/api/machine"
	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	logf "sigs.k8s.io/cluster-api/cmd/clusterctl/log"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	controlplanev1 "github.com/talos-systems/cluster-api-control-plane-provider-talos/api/v1alpha3"
)

var talosVersion *semver.Version

type clusterctlConfig struct {
	Providers []providerConfig `yaml:"providers"`
}

type providerConfig struct {
	Name         string                 `yaml:"name"`
	URL          string                 `yaml:"url"`
	ProviderType clusterv1.ProviderType `yaml:"type"`
}

type IntegrationSuite struct {
	suite.Suite

	manager         *capi.Manager
	cluster         *capi.Cluster
	ctx             context.Context
	cancel          context.CancelFunc
	finalK8sVersion string
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

	providerType := env("PROVIDER", "aws:v1.1.0")
	suite.finalK8sVersion = os.Getenv("UPGRADE_K8S_VERSION")

	provider, err := infrastructure.NewProvider(providerType)
	suite.Require().NoError(err)

	var (
		clusterctlConfigPath string
		clusterctlConfigs    = &clusterctlConfig{}
	)

	for _, config := range []struct {
		env          string
		providerType clusterv1.ProviderType
		url          string
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

		if customConfig != "" || config.url != "" {
			if clusterctlConfigs.Providers == nil {
				clusterctlConfigs.Providers = []providerConfig{}
			}

			url := fmt.Sprintf("file://%s", customConfig)

			if config.url != "" {
				url = config.url
			}

			clusterctlConfigs.Providers = append(clusterctlConfigs.Providers, providerConfig{
				Name:         "talos",
				URL:          url,
				ProviderType: config.providerType,
			})
		}
	}

	if clusterctlConfigs.Providers != nil {
		config, configError := os.CreateTemp("", "clusterctlConfig*.yaml")

		suite.Require().NoError(configError)

		defer os.Remove(config.Name()) //nolint:errcheck

		clusterctlConfigPath = config.Name()

		encoder := yaml.NewEncoder(config)
		suite.Require().NoError(encoder.Encode(clusterctlConfigs))
		suite.Require().NoError(encoder.Close())
	}

	options := capi.Options{
		CoreProvider:            env("CORE_PROVIDER", "cluster-api"),
		BootstrapProviders:      []string{"talos"},
		InfrastructureProviders: []infrastructure.Provider{provider},
		ControlPlaneProviders:   []string{"talos"},
		WaitProviderTimeout:     time.Minute,
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
		capi.WithKubernetesVersion(strings.TrimLeft(env("WORKLOAD_KUBERNETES_VERSION", env("K8S_VERSION", "v1.22.2")), "v")),
		capi.WithTemplateFile("https://github.com/siderolabs/cluster-api-templates/blob/main/aws/standard/standard.yaml"),
		capi.WithControlPlaneNodes(3),
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

// Test01ReconcileMachine remove a machine and wait until cluster gets healthy again.
func (suite *IntegrationSuite) Test01ReconcileMachine() {
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

		talosclient, etcdErr := suite.cluster.TalosClient(suite.ctx)
		if etcdErr != nil {
			return nil, etcdErr
		}

		resp, etcdErr := talosclient.EtcdMemberList(suite.ctx, &machineapi.EtcdMemberListRequest{})
		if etcdErr != nil {
			return nil, etcdErr
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

func (suite *IntegrationSuite) Test02UpgradeK8s() {
	if suite.finalK8sVersion == "" {
		suite.T().Skip("K8s upgrade version is not set")
	}

	client, err := suite.manager.GetClient(suite.ctx)
	suite.Require().NoError(err)

	controlplane, err := suite.cluster.ControlPlanes(suite.ctx)
	suite.Require().NoError(err)

	patchHelper, err := patch.NewHelper(controlplane, client)
	suite.Require().NoError(err)

	suite.Require().NoError(unstructured.SetNestedField(controlplane.Object, suite.finalK8sVersion, "spec", "version"))

	suite.Require().NoError(patchHelper.Patch(suite.ctx, controlplane))

	suite.Require().NoError(err)

	selector, err := labels.Parse("cluster.x-k8s.io/control-plane")
	suite.Require().NoError(err)

	err = retry.Constant(time.Minute*20, retry.WithUnits(time.Second)).Retry(func() error {
		machines := unstructured.UnstructuredList{}
		machines.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Kind:    "Machine",
			Version: suite.manager.Version(),
		})

		if err = client.List(suite.ctx, &machines, &runtimeclient.MatchingLabelsSelector{Selector: selector}); err != nil {
			return err
		}

		for _, machine := range machines.Items {
			var (
				machineVersion string
				found          bool
			)

			machineVersion, found, err = unstructured.NestedString(machine.Object, "spec", "version")
			if err != nil {
				return err
			}

			if !found {
				return retry.ExpectedErrorf("version wasn't found for the machine")
			}

			if machineVersion != suite.finalK8sVersion {
				return retry.ExpectedErrorf("one of the machines is still using an old Kubernetes version")
			}
		}

		return nil
	})

	suite.Require().NoError(err)
}

// Test02ScaleDown scale control planes down.
func (suite *IntegrationSuite) Test03ScaleDown() {
	suite.Require().NoError(suite.cluster.Sync(suite.ctx))

	err := suite.cluster.Scale(suite.ctx, 1, capi.ControlPlaneNodes)
	suite.Require().NoError(err)

	time.Sleep(2 * time.Second)

	suite.Require().NoError(suite.cluster.Sync(suite.ctx))
}

// Test03ScaleControlPlaneNoWait scale control plane nodes up and down without waiting.
func (suite *IntegrationSuite) Test04ScaleControlPlaneNoWait() {
	ctx, cancel := context.WithCancel(suite.ctx)

	go func() {
		time.Sleep(time.Second * 5)

		cancel()
	}()

	suite.cluster.Scale(ctx, 3, capi.ControlPlaneNodes) //nolint:errcheck

	err := retry.Constant(time.Second*10, retry.WithUnits(time.Second)).Retry(func() error {
		if err := suite.cluster.Scale(suite.ctx, 1, capi.ControlPlaneNodes); err != nil {
			if apierrors.IsConflict(err) {
				return retry.ExpectedError(err)
			}

			return err
		}

		return nil
	})

	suite.Require().NoError(err)
}

// Test04ScaleControlPlaneToZero try to scale control plane to zero and check that it never does that.
func (suite *IntegrationSuite) Test05ScaleControlPlaneToZero() {
	ctx, cancel := context.WithCancel(suite.ctx)

	go func() {
		time.Sleep(time.Second * 5)

		cancel()
	}()

	suite.cluster.Scale(ctx, 0, capi.ControlPlaneNodes) //nolint:errcheck

	err := retry.Constant(time.Second*20, retry.WithUnits(time.Second)).Retry(func() error {
		controlplane, err := suite.cluster.ControlPlanes(suite.ctx)
		if err != nil {
			return err
		}

		var tcp controlplanev1.TalosControlPlane

		err = runtime.DefaultUnstructuredConverter.
			FromUnstructured(controlplane.UnstructuredContent(), &tcp)
		if err != nil {
			return err
		}

		if !conditions.Has(&tcp, controlplanev1.ResizedCondition) &&
			conditions.GetMessage(&tcp, controlplanev1.ResizedCondition) != "Cannot scale down control plane nodes to 0" {
			return retry.ExpectedErrorf("node resized conditions error status hasn't updated")
		}

		return nil
	})

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

	suite.Run(t, new(IntegrationSuite))
}
