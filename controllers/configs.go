// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"fmt"
	"reflect"

	"github.com/pkg/errors"
	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// talosconfigForMachine will generate a talosconfig that uses *all* found addresses as the endpoints.
func (r *TalosControlPlaneReconciler) talosconfigForMachines(ctx context.Context, tcp *controlplanev1.TalosControlPlane, machines ...clusterv1.Machine) (*talosclient.Client, error) {
	if len(machines) == 0 {
		return nil, fmt.Errorf("at least one machine should be provided")
	}

	clusterName := tcp.GetLabels()["cluster.x-k8s.io/cluster-name"]

	for _, ref := range tcp.GetOwnerReferences() {
		if ref.Kind != "Cluster" {
			continue
		}

		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		if gv.Group == clusterv1.GroupVersion.Group {
			clusterName = ref.Name

			break
		}
	}

	if clusterName == "" {
		return nil, fmt.Errorf("failed to determine the cluster name of the control plane")
	}

	if !reflect.ValueOf(tcp.Spec.ControlPlaneConfig.InitConfig).IsZero() {
		return r.talosconfigFromWorkloadCluster(ctx, client.ObjectKey{Namespace: tcp.GetNamespace(), Name: clusterName}, machines...)
	}

	addrList := []string{}

	var (
		talosconfigSecret v1.Secret
	)

	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Namespace: tcp.GetNamespace(),
			Name:      clusterName + "-talosconfig",
		},
		&talosconfigSecret,
	); err != nil {
		return nil, err
	}

	t, err := talosconfig.FromBytes(talosconfigSecret.Data["talosconfig"])
	if err != nil {
		return nil, err
	}

	for _, machine := range machines {
		for _, addr := range machine.Status.Addresses {
			if addr.Type == clusterv1.MachineExternalIP || addr.Type == clusterv1.MachineInternalIP {
				addrList = append(addrList, addr.Address)
			}
		}

		if len(addrList) == 0 {
			return nil, fmt.Errorf("no addresses were found for node %q", machine.Name)
		}
	}

	// we don't need to set endpoints in general here, as endpoints were already pre-populated by the CABPT controller
	// but we use the `machines` to _limit_ access to a specific machine in some places, and we need to be compatible
	// with talosconfigFromWorkloadCluster which doesn't rely on Machine's Addresses
	//
	// once we're done with Sidero and `init` nodes, we can switch to use `WithNodes` and proper Machine IPs
	return talosclient.New(ctx, talosclient.WithEndpoints(addrList...), talosclient.WithConfig(t))
}

// talosconfigFromWorkloadCluster gets talosconfig and populates endoints using workload cluster nodes.
func (r *TalosControlPlaneReconciler) talosconfigFromWorkloadCluster(ctx context.Context, cluster client.ObjectKey, machines ...clusterv1.Machine) (*talosclient.Client, error) {
	if len(machines) == 0 {
		return nil, fmt.Errorf("at least one machine should be provided")
	}

	c, err := r.Tracker.GetClient(ctx, cluster)
	if err != nil {
		return nil, err
	}

	addrList := []string{}

	var t *talosconfig.Config

	for _, machine := range machines {
		if machine.Status.NodeRef == nil {
			return nil, fmt.Errorf("%q machine does not have a nodeRef", machine.Name)
		}

		var node v1.Node

		// grab all addresses as endpoints
		err := c.Get(ctx, types.NamespacedName{Name: machine.Status.NodeRef.Name, Namespace: machine.Status.NodeRef.Namespace}, &node)
		if err != nil {
			return nil, err
		}

		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeExternalIP || addr.Type == corev1.NodeInternalIP {
				addrList = append(addrList, addr.Address)
			}
		}

		if len(addrList) == 0 {
			return nil, fmt.Errorf("no addresses were found for node %q", node.Name)
		}

		if t == nil {
			var (
				cfgs  cabptv1.TalosConfigList
				found *cabptv1.TalosConfig
			)

			// find talosconfig in the machine's namespace
			err = r.Client.List(ctx, &cfgs, client.InNamespace(machine.Namespace))
			if err != nil {
				return nil, err
			}

			for _, cfg := range cfgs.Items {
				for _, ref := range cfg.OwnerReferences {
					if ref.Kind == "Machine" && ref.Name == machine.Name {
						found = &cfg
						break
					}
				}
			}

			if found == nil {
				return nil, fmt.Errorf("failed to find TalosConfig for %q", machine.Name)
			}

			t, err = talosconfig.FromString(found.Status.TalosConfig)
			if err != nil {
				return nil, err
			}
		}
	}

	return talosclient.New(ctx, talosclient.WithEndpoints(addrList...), talosclient.WithConfig(t))
}
