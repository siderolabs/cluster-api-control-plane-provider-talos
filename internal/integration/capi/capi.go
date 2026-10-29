// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package capi manages CAPI installation, provides default client for CAPI CRDs.
//
// It was forked from the deprecated github.com/siderolabs/capi-utils for the integration tests.
package capi

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/cluster"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/siderolabs/cluster-api-control-plane-provider-talos/internal/integration/capi/infrastructure"
)

// Manager installs and controls cluster API installation.
type Manager struct {
	kubeconfig    client.Kubeconfig
	client        client.Client
	clientset     *kubernetes.Clientset
	config        *rest.Config
	runtimeClient runtimeclient.Client
	version       string
	providers     []infrastructure.Provider
	cfg           *Config

	options Options
}

// Options for the CAPI installer.
type Options struct {
	Proxy                   cluster.Proxy
	Kubeconfig              client.Kubeconfig
	ClusterctlConfigPath    string
	CoreProvider            string
	ContextName             string
	InfrastructureProviders []infrastructure.Provider
	BootstrapProviders      []string
	ControlPlaneProviders   []string
	WaitProviderTimeout     time.Duration
}

// NewManager creates new Manager object.
func NewManager(ctx context.Context, options Options) (*Manager, error) {
	clusterAPI := &Manager{
		options: options,
		cfg:     newConfig(),
	}

	err := clusterAPI.cfg.Init(ctx, options.ClusterctlConfigPath)
	if err != nil {
		return nil, err
	}

	configClient, err := config.New(ctx, options.ClusterctlConfigPath, config.InjectReader(clusterAPI.cfg))
	if err != nil {
		return nil, err
	}

	opts := []client.Option{
		client.InjectConfig(configClient),
	}

	if options.Proxy != nil {
		opts = append(opts, client.InjectClusterClientFactory(func(input client.ClusterClientFactoryInput) (cluster.Client, error) {
			return cluster.New(
				cluster.Kubeconfig(input.Kubeconfig),
				configClient,
				cluster.InjectYamlProcessor(input.Processor),
				cluster.InjectProxy(options.Proxy),
			), nil
		}))
	}

	clusterAPI.client, err = client.New(ctx, options.ClusterctlConfigPath, opts...)
	if err != nil {
		return nil, err
	}

	if options.Proxy != nil {
		clusterAPI.config, err = options.Proxy.GetConfig()
		if err != nil {
			return nil, err
		}
	} else {
		var clusterConfig client.Kubeconfig

		clusterConfig, err = clusterAPI.GetKubeconfig(ctx)
		if err != nil {
			return nil, err
		}

		clusterAPI.config, err = clientcmd.BuildConfigFromKubeconfigGetter("", func() (*clientcmdapi.Config, error) {
			c, e := clientcmd.LoadFromFile(clusterConfig.Path)
			if e != nil {
				return nil, e
			}

			if clusterAPI.options.ContextName == "" {
				clusterAPI.options.ContextName = c.CurrentContext
			}

			return c, nil
		})
		if err != nil {
			return nil, err
		}
	}

	clusterAPI.clientset, err = kubernetes.NewForConfig(clusterAPI.config)
	if err != nil {
		return nil, err
	}

	_, err = clusterAPI.GetClient(ctx)
	if err != nil {
		return nil, err
	}

	if err = clusterAPI.FetchState(ctx); err != nil {
		return nil, err
	}

	return clusterAPI, nil
}

// GetKubeconfig returns kubeconfig in clusterctl expected format.
func (clusterAPI *Manager) GetKubeconfig(context.Context) (client.Kubeconfig, error) {
	if clusterAPI.kubeconfig.Path != "" {
		return clusterAPI.kubeconfig, nil
	}

	var path string

	if v := os.Getenv(clientcmd.RecommendedConfigPathEnvVar); v != "" {
		path = v
	} else {
		usr, err := user.Current()
		if err != nil {
			return client.Kubeconfig{}, err
		}

		path = filepath.Join(usr.HomeDir, clientcmd.RecommendedHomeDir, clientcmd.RecommendedFileName)
	}

	clusterAPI.kubeconfig.Path = path
	clusterAPI.kubeconfig.Context = clusterAPI.options.ContextName

	return clusterAPI.kubeconfig, nil
}

// GetManagerClient client returns instance of cluster API client.
func (clusterAPI *Manager) GetManagerClient() client.Client {
	return clusterAPI.client
}

// GetClient returns k8s client stuffed with CAPI CRDs.
func (clusterAPI *Manager) GetClient(context.Context) (client runtimeclient.Client, err error) {
	if clusterAPI.runtimeClient != nil {
		return clusterAPI.runtimeClient, nil
	}

	clusterAPI.runtimeClient, err = GetMetalClient(clusterAPI.config)

	return clusterAPI.runtimeClient, err
}

// GetClientSet returns a kubernetes clientset to use.
func (clusterAPI *Manager) GetClientSet() *kubernetes.Clientset {
	return clusterAPI.clientset
}

// Install the Manager components and wait for them to be ready.
func (clusterAPI *Manager) Install(ctx context.Context) error {
	kubeconfig, err := clusterAPI.GetKubeconfig(ctx)
	if err != nil {
		return err
	}

	// nb: We use the same call to Manager.Install for both core and infra installs
	// This check ensures we don't try to install core if the provider string is empty,
	// which it would be during an infra install
	if clusterAPI.options.CoreProvider != "" {
		err = clusterAPI.InstallCore(ctx, kubeconfig)
		if err != nil {
			return err
		}
	}

	for _, provider := range clusterAPI.options.InfrastructureProviders {
		err = clusterAPI.InstallProvider(ctx, kubeconfig, provider)
		if err != nil {
			return err
		}
	}

	for _, provider := range clusterAPI.options.InfrastructureProviders {
		if err = provider.WaitReady(ctx, clusterAPI.clientset); err != nil {
			return err
		}
	}

	return clusterAPI.FetchState(ctx)
}

// InstallCore installs only core, global watched components (capi, cabpt, cacppt).
//
// Each requested provider is looked up in the clusterctl provider inventory, and only the missing
// ones are passed to clusterctl Init. A retry after a partially failed Init (e.g. a provider wait
// timeout after capi-system was created) therefore installs what is still missing instead of
// skipping the whole step.
func (clusterAPI *Manager) InstallCore(ctx context.Context, kubeconfig client.Kubeconfig) error {
	inventory, err := clusterAPI.listProviderInventory(ctx)
	if err != nil {
		return err
	}

	var coreProvider string

	if clusterAPI.options.CoreProvider != "" && !inventory.has(clusterAPI.options.CoreProvider, clusterctlv1.CoreProviderType) {
		coreProvider = clusterAPI.options.CoreProvider
	}

	bootstrapProviders := inventory.missing(clusterAPI.options.BootstrapProviders, clusterctlv1.BootstrapProviderType)
	controlPlaneProviders := inventory.missing(clusterAPI.options.ControlPlaneProviders, clusterctlv1.ControlPlaneProviderType)

	if coreProvider != "" || len(bootstrapProviders) > 0 || len(controlPlaneProviders) > 0 {
		fmt.Println("initializing the core capi components")

		// When the core provider is not installed yet, clusterctl Init adds the kubeadm bootstrap and
		// control plane providers to an empty list, so opt out of that explicitly.
		if coreProvider != "" {
			if len(bootstrapProviders) == 0 {
				bootstrapProviders = []string{client.NoopProvider}
			}

			if len(controlPlaneProviders) == 0 {
				controlPlaneProviders = []string{client.NoopProvider}
			}
		}

		// Initialize everything but the infra providers, as we want to specify target
		// namespaces for those.
		coreOpts := client.InitOptions{
			Kubeconfig:              kubeconfig,
			CoreProvider:            coreProvider,
			BootstrapProviders:      bootstrapProviders,
			ControlPlaneProviders:   controlPlaneProviders,
			InfrastructureProviders: []string{},
			TargetNamespace:         "",
			LogUsageInstructions:    false,
		}

		if clusterAPI.options.WaitProviderTimeout != 0 {
			coreOpts.WaitProviders = true
			coreOpts.WaitProviderTimeout = time.Minute * 5
		}

		if _, err = clusterAPI.client.Init(ctx, coreOpts); err != nil {
			return err
		}
	}

	if clusterAPI.options.WaitProviderTimeout == 0 {
		return nil
	}

	// A provider can be in the inventory while its controller never became available (the Init that
	// installed it failed waiting for it), so check the controllers here as well.
	if inventory, err = clusterAPI.listProviderInventory(ctx); err != nil {
		return err
	}

	for _, p := range []struct {
		providerType clusterctlv1.ProviderType
		names        []string
	}{
		{clusterctlv1.CoreProviderType, []string{clusterAPI.options.CoreProvider}},
		{clusterctlv1.BootstrapProviderType, clusterAPI.options.BootstrapProviders},
		{clusterctlv1.ControlPlaneProviderType, clusterAPI.options.ControlPlaneProviders},
	} {
		for _, name := range p.names {
			if name == "" || name == client.NoopProvider {
				continue
			}

			if err = clusterAPI.checkProviderAvailable(ctx, inventory, name, p.providerType); err != nil {
				return err
			}
		}
	}

	return nil
}

// InstallProvider installs a specific infrastructure provider and allows namespacing of
// the provider itself and its "watches".
func (clusterAPI *Manager) InstallProvider(ctx context.Context, kubeconfig client.Kubeconfig, provider infrastructure.Provider) error {
	var (
		installed bool
		err       error
	)

	providerString := provider.Name()

	if provider.Version() != "" {
		providerString += ":" + provider.Version()
	}

	if installed, err = provider.IsInstalled(ctx, clusterAPI.clientset); err != nil {
		return err
	}

	// clusterctl Init refuses to install a provider that is already in the inventory, which happens
	// on a retry after an Init that installed the provider but failed waiting for it.
	if !installed {
		inventory, err := clusterAPI.listProviderInventory(ctx)
		if err != nil {
			return err
		}

		installed = inventory.has(provider.Name(), clusterctlv1.InfrastructureProviderType)
	}

	if !installed {
		fmt.Printf("initializing infrastructure provider %s\n", providerString)

		vars, err := provider.ProviderVars()
		if err != nil {
			return err
		}

		clusterAPI.patchConfig(vars)

		infraOpts := client.InitOptions{
			Kubeconfig:              kubeconfig,
			CoreProvider:            "",
			BootstrapProviders:      []string{},
			ControlPlaneProviders:   []string{},
			InfrastructureProviders: []string{providerString},
			TargetNamespace:         provider.Namespace(),
			LogUsageInstructions:    false,
		}

		if clusterAPI.options.WaitProviderTimeout != 0 {
			infraOpts.WaitProviders = true
			infraOpts.WaitProviderTimeout = time.Minute * 5
		}

		if _, err = clusterAPI.client.Init(ctx, infraOpts); err != nil {
			return err
		}
	}

	return nil
}

// FetchState fetches infra providers and installed CAPI version if any.
//
//nolint:gocognit
func (clusterAPI *Manager) FetchState(ctx context.Context) error {
	resources, err := clusterAPI.clientset.ServerPreferredResources()
	if err != nil {
		return err
	}

	var gvProvider, gvCluster schema.GroupVersion

	for _, list := range resources {
		for _, resource := range list.APIResources {
			switch resource.Kind {
			case "Provider":
				gvProvider, err = schema.ParseGroupVersion(list.GroupVersion)
				if err != nil {
					return err
				}
			case "Cluster":
				gvCluster, err = schema.ParseGroupVersion(list.GroupVersion)
				if err != nil {
					return err
				}
			}
		}
	}

	// Assume CAPI not installed
	if gvProvider.Version == "" {
		return nil
	}

	providers := &unstructured.UnstructuredList{}
	providers.SetGroupVersionKind(schema.GroupVersionKind{
		Kind:    "Provider",
		Group:   gvProvider.Group,
		Version: gvProvider.Version,
	})

	if err = clusterAPI.runtimeClient.List(ctx, providers); err != nil {
		return fmt.Errorf("failed to list providers %w", err)
	}

	var (
		providerName    string
		providerVersion string
		providerType    string
		ok              bool
	)

	infrastructureProviders := []infrastructure.Provider{}

	for _, provider := range providers.Items {
		if providerType, ok, err = unstructured.NestedString(provider.Object, "type"); err != nil {
			return err
		} else if !ok {
			return fieldNotFound("type")
		}

		if clusterctlv1.ProviderType(providerType) == clusterctlv1.InfrastructureProviderType {
			if providerName, ok, err = unstructured.NestedString(provider.Object, "providerName"); err != nil {
				return err
			} else if !ok {
				return fieldNotFound("providerName")
			}

			if providerVersion, ok, err = unstructured.NestedString(provider.Object, "version"); err != nil {
				return err
			} else if !ok {
				return fieldNotFound("providerVersion")
			}

			provider, err := infrastructure.NewProvider(fmt.Sprintf("%s:%s", providerName, providerVersion))
			// if we couldn't parse it then it's not supported
			if err != nil {
				continue
			}

			infrastructureProviders = append(infrastructureProviders, provider)
		}
	}

	clusterAPI.providers = infrastructureProviders
	clusterAPI.version = gvCluster.Version

	return nil
}

// Version returns installed CAPI version.
func (clusterAPI *Manager) Version() string {
	return clusterAPI.version
}

func (clusterAPI *Manager) patchConfig(vars infrastructure.Variables) {
	for key, value := range vars {
		if value != "" {
			clusterAPI.cfg.Set(key, value)
		}
	}
}

type ref struct {
	types.NamespacedName
	gvk schema.GroupVersionKind
}

// getRef resolves a reference field, supporting both the legacy v1beta1
// ObjectReference shape (name, namespace, apiVersion, kind) and the CAPI
// v1beta2 ContractVersionedObjectReference shape (name, kind, apiGroup),
// which dropped namespace (always same as the referrer) and apiVersion
// (the served version must be resolved via discovery instead).
func getRef(clusterAPI *Manager, in map[string]any, keys ...string) (*ref, error) {
	res := &ref{}

	refInterface, found, err := unstructured.NestedMap(in, keys...)
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fieldNotFound(keys...)
	}

	res.Name, found, err = unstructured.NestedString(refInterface, "name")
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fieldNotFound(append(keys, "name")...)
	}

	res.Namespace, found, err = unstructured.NestedString(refInterface, "namespace")
	if err != nil {
		return nil, err
	}

	if !found {
		if res.Namespace, _, err = unstructured.NestedString(in, "metadata", "namespace"); err != nil {
			return nil, err
		}
	}

	kind, found, err := unstructured.NestedString(refInterface, "kind")
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fieldNotFound(append(keys, "kind")...)
	}

	groupVersion, found, err := unstructured.NestedString(refInterface, "apiVersion")
	if err != nil {
		return nil, err
	}

	if found {
		res.gvk = schema.FromAPIVersionAndKind(groupVersion, kind)

		return res, nil
	}

	apiGroup, found, err := unstructured.NestedString(refInterface, "apiGroup")
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fieldNotFound(append(keys, "apiVersion")...)
	}

	version, err := clusterAPI.preferredVersion(apiGroup, kind)
	if err != nil {
		return nil, err
	}

	res.gvk = schema.GroupVersionKind{Group: apiGroup, Version: version, Kind: kind}

	return res, nil
}

// preferredVersion resolves the server-preferred served version for a given
// group/kind, mirroring how FetchState resolves the Cluster/Provider version.
func (clusterAPI *Manager) preferredVersion(group, kind string) (string, error) {
	resources, err := clusterAPI.clientset.ServerPreferredResources()
	if err != nil {
		return "", err
	}

	for _, list := range resources {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}

		if gv.Group != group {
			continue
		}

		for _, resource := range list.APIResources {
			if resource.Kind == kind {
				return gv.Version, nil
			}
		}
	}

	return "", fmt.Errorf("failed to find preferred version for %s/%s", group, kind)
}

func fieldNotFound(fields ...string) error {
	return fmt.Errorf("failed to find field %s", strings.Join(fields, "."))
}

// providerInventory is the list of providers recorded in the clusterctl inventory
// (providers.clusterctl.cluster.x-k8s.io).
type providerInventory []clusterctlv1.Provider

// listProviderInventory lists the clusterctl provider inventory. It is empty before the first Init,
// when the inventory CRD does not exist yet.
func (clusterAPI *Manager) listProviderInventory(ctx context.Context) (providerInventory, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(clusterctlv1.GroupVersion.WithKind("ProviderList"))

	if err := clusterAPI.runtimeClient.List(ctx, list); err != nil {
		if meta.IsNoMatchError(err) || errors.IsNotFound(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to list the clusterctl provider inventory: %w", err)
	}

	inventory := make(providerInventory, 0, len(list.Items))

	for _, item := range list.Items {
		var provider clusterctlv1.Provider

		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &provider); err != nil {
			return nil, err
		}

		inventory = append(inventory, provider)
	}

	return inventory, nil
}

// get returns the inventory entry for a provider, given as "name" or "name:version".
func (inventory providerInventory) get(provider string, providerType clusterctlv1.ProviderType) *clusterctlv1.Provider {
	name, _, _ := strings.Cut(provider, ":")

	for i := range inventory {
		if inventory[i].ProviderName == name && inventory[i].GetProviderType() == providerType {
			return &inventory[i]
		}
	}

	return nil
}

func (inventory providerInventory) has(provider string, providerType clusterctlv1.ProviderType) bool {
	return inventory.get(provider, providerType) != nil
}

// missing returns the providers that are not in the inventory yet.
func (inventory providerInventory) missing(providers []string, providerType clusterctlv1.ProviderType) []string {
	var res []string

	for _, provider := range providers {
		if !inventory.has(provider, providerType) {
			res = append(res, provider)
		}
	}

	return res
}

// checkProviderAvailable verifies that a provider is in the inventory and that all of its
// controller Deployments are available.
func (clusterAPI *Manager) checkProviderAvailable(ctx context.Context, inventory providerInventory, name string, providerType clusterctlv1.ProviderType) error {
	provider := inventory.get(name, providerType)
	if provider == nil {
		return fmt.Errorf("provider %s (%s) is not installed", name, providerType)
	}

	deployments, err := clusterAPI.clientset.AppsV1().Deployments(provider.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{clusterv1.ProviderNameLabel: provider.ManifestLabel()}).String(),
	})
	if err != nil {
		return err
	}

	if len(deployments.Items) == 0 {
		return fmt.Errorf("no controller deployments found for provider %s in namespace %s", provider.ManifestLabel(), provider.Namespace)
	}

	for _, deployment := range deployments.Items {
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}

		if deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.AvailableReplicas < replicas {
			return fmt.Errorf("deployment %s/%s of provider %s is not available: %d of %d replicas available",
				deployment.Namespace, deployment.Name, provider.ManifestLabel(), deployment.Status.AvailableReplicas, replicas)
		}
	}

	return nil
}
