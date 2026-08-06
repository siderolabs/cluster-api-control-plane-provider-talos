// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package capi

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/siderolabs/go-retry/retry"
	talosclusterapi "github.com/siderolabs/talos/pkg/machinery/api/cluster"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	capiclient "sigs.k8s.io/cluster-api/cmd/clusterctl/client"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Cluster attaches to the provisioned CAPI cluster and provides talos.Cluster.
type Cluster struct {
	manager           *Manager
	client            *talosclient.Client
	clientConfig      *clientconfig.Config
	cluster           unstructured.Unstructured
	name              string
	namespace         string
	controlPlaneNodes []string
	workerNodes       []string
}

// NewCluster fetches cluster info from the CAPI state.
func (clusterAPI *Manager) NewCluster(ctx context.Context, name, namespace string) (*Cluster, error) {
	res := &Cluster{
		manager:   clusterAPI,
		name:      name,
		namespace: namespace,
	}

	if err := res.sync(ctx); err != nil {
		return nil, err
	}

	return res, nil
}

// Sync updates nodes pool and recreates talos client.
//
//nolint:gocyclo,cyclop
func (cluster *Cluster) Sync(ctx context.Context) error {
	var (
		controlPlane unstructured.Unstructured
		machines     unstructured.UnstructuredList

		controlPlaneNodes = []string{}
		workerNodes       = []string{}
		configEndpoints   = []string{}
	)

	machines.SetGroupVersionKind(
		schema.GroupVersionKind{
			Version: cluster.manager.version,
			Group:   "cluster.x-k8s.io",
			Kind:    "Machine",
		},
	)

	var (
		controlPlaneSelector string
		found                bool
		err                  error
	)

	controlPlaneRef, err := getRef(cluster.cluster.Object, "spec", "controlPlaneRef")
	if err != nil {
		return err
	}

	controlPlane.SetGroupVersionKind(controlPlaneRef.gvk)

	if err = cluster.manager.runtimeClient.Get(ctx, controlPlaneRef.NamespacedName, &controlPlane); err != nil {
		return err
	}

	if controlPlaneSelector, found, err = unstructured.NestedString(controlPlane.Object, "status", "selector"); err != nil {
		return err
	} else if !found {
		return fieldNotFound("status", "selector")
	}

	labelSelector, err := labels.Parse(controlPlaneSelector)
	if err != nil {
		return err
	}

	if err = cluster.manager.runtimeClient.List(ctx, &machines, runtimeclient.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return err
	}

	if len(machines.Items) < 1 {
		return fmt.Errorf("not enough machines found")
	}

	var talosConfig v1.Secret

	if err = cluster.manager.runtimeClient.Get(ctx, types.NamespacedName{
		Namespace: cluster.Namespace(),
		Name:      cluster.Name() + "-talosconfig",
	}, &talosConfig); err != nil {
		return err
	}

	cluster.clientConfig, err = clientconfig.FromBytes(talosConfig.Data["talosconfig"])
	if err != nil {
		return err
	}

	kubeconfig, err := cluster.manager.GetKubeconfig(ctx)
	if err != nil {
		return err
	}

	options := capiclient.GetKubeconfigOptions{
		Kubeconfig:          kubeconfig,
		WorkloadClusterName: cluster.name,
		Namespace:           cluster.namespace,
	}

	raw, err := cluster.manager.client.GetKubeconfig(ctx, options)
	if err != nil {
		return err
	}

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(raw))
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		_, controlPlane := node.Labels["node-role.kubernetes.io/control-plane"]

		for _, address := range node.Status.Addresses {
			switch {
			case address.Type == v1.NodeExternalIP && controlPlane:
				configEndpoints = append(configEndpoints, address.Address)
			case address.Type == v1.NodeInternalIP && controlPlane:
				controlPlaneNodes = append(controlPlaneNodes, address.Address)
			case address.Type == v1.NodeInternalIP:
				workerNodes = append(workerNodes, address.Address)
			}
		}
	}

	if len(configEndpoints) < 1 {
		return fmt.Errorf("failed to find control plane nodes")
	}

	cluster.clientConfig.Contexts[cluster.clientConfig.Context].Endpoints = configEndpoints

	var talosClient *talosclient.Client

	talosClient, err = talosclient.New(ctx, talosclient.WithConfig(cluster.clientConfig))
	if err != nil {
		return err
	}

	cluster.client = talosClient
	cluster.controlPlaneNodes = controlPlaneNodes
	cluster.workerNodes = workerNodes

	return nil
}

// TalosClient returns new talos client for the CAPI cluster.
func (cluster *Cluster) TalosClient(ctx context.Context) (*talosclient.Client, error) {
	if cluster.client != nil {
		return cluster.client, nil
	}

	if err := cluster.Sync(ctx); err != nil {
		return nil, err
	}

	return cluster.client, nil
}

// TalosConfig returns talosconfig for the cluster.
func (cluster *Cluster) TalosConfig(ctx context.Context) (*clientconfig.Config, error) {
	if cluster.clientConfig != nil {
		return cluster.clientConfig, nil
	}

	if err := cluster.Sync(ctx); err != nil {
		return nil, err
	}

	return cluster.clientConfig, nil
}

// Health runs the healthcheck for the cluster.
func (cluster *Cluster) Health(ctx context.Context) error {
	return retry.Constant(5*time.Minute, retry.WithUnits(10*time.Second)).RetryWithContext(ctx, func(ctx context.Context) error {
		// retry health checks as sometimes bootstrap bootkube issues break the check
		return retry.ExpectedError(cluster.health(ctx))
	})
}

func (cluster *Cluster) health(ctx context.Context) error {
	client, err := cluster.TalosClient(ctx)
	if err != nil {
		return err
	}

	resp, err := client.ClusterHealthCheck(talosclient.WithNodes(ctx, cluster.controlPlaneNodes[0]), 3*time.Minute, &talosclusterapi.ClusterInfo{
		ControlPlaneNodes: cluster.controlPlaneNodes,
		WorkerNodes:       cluster.workerNodes,
	})
	if err != nil {
		return err
	}

	if err := resp.CloseSend(); err != nil {
		return err
	}

	for {
		msg, err := resp.Recv()
		if err != nil {
			if err == io.EOF || status.Code(err) == codes.Canceled { //nolint:errorlint
				return nil
			}

			return err
		}

		if msg.GetMetadata().GetError() != "" {
			return fmt.Errorf("healthcheck error: %s", msg.GetMetadata().GetError())
		}

		fmt.Fprintln(os.Stderr, msg.GetMessage())
	}
}

// Name of the cluster.
func (cluster *Cluster) Name() string {
	return cluster.name
}

// Namespace of the cluster.
func (cluster *Cluster) Namespace() string {
	return cluster.namespace
}

// ControlPlanes gets controlplane object from the management cluster.
func (cluster *Cluster) ControlPlanes(ctx context.Context) (*unstructured.Unstructured, error) {
	controlPlaneRef, err := getRef(cluster.cluster.Object, "spec", "controlPlaneRef")
	if err != nil {
		return nil, err
	}

	var controlPlane unstructured.Unstructured

	controlPlane.SetGroupVersionKind(controlPlaneRef.gvk)

	if err = cluster.manager.runtimeClient.Get(ctx, controlPlaneRef.NamespacedName, &controlPlane); err != nil {
		return nil, err
	}

	return &controlPlane, nil
}

// Workers gets MachineDeployment list from the management cluster.
func (cluster *Cluster) Workers(ctx context.Context) (*unstructured.UnstructuredList, error) {
	var machineDeployments unstructured.UnstructuredList

	machineDeployments.SetGroupVersionKind(
		schema.GroupVersionKind{
			Version: cluster.manager.version,
			Group:   "cluster.x-k8s.io",
			Kind:    "MachineDeployment",
		},
	)

	labelSelector, err := labels.Parse(fmt.Sprintf("cluster.x-k8s.io/cluster-name=%s", cluster.name))
	if err != nil {
		return nil, err
	}

	if err = cluster.manager.runtimeClient.List(ctx, &machineDeployments, runtimeclient.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return nil, err
	}

	return &machineDeployments, nil
}

func (cluster *Cluster) sync(ctx context.Context) error {
	cluster.cluster.SetGroupVersionKind(
		schema.GroupVersionKind{
			Version: cluster.manager.version,
			Group:   "cluster.x-k8s.io",
			Kind:    "Cluster",
		},
	)

	clusterRef := types.NamespacedName{Namespace: cluster.namespace, Name: cluster.name}

	return cluster.manager.runtimeClient.Get(ctx, clusterRef, &cluster.cluster)
}
