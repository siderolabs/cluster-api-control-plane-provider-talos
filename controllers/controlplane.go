// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"reflect"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2/klogr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Log is the global logger for the internal package.
var Log = klogr.New()

// ControlPlane holds business logic around control planes.
// It should never need to connect to a service, that responsibility lies outside of this struct.
type ControlPlane struct {
	TCP      *controlplanev1.TalosControlPlane
	Cluster  *clusterv1.Cluster
	Machines collections.Machines

	infraObjects map[string]*unstructured.Unstructured
	talosConfigs map[string]*cabptv1.TalosConfig
}

// newControlPlane returns an instantiated ControlPlane.
func newControlPlane(ctx context.Context, client client.Client, cluster *clusterv1.Cluster, tcp *controlplanev1.TalosControlPlane, machines collections.Machines) (*ControlPlane, error) {
	infraObjects, err := getInfraResources(ctx, client, machines)
	if err != nil {
		return nil, err
	}

	talosConfigs, err := getTalosConfigs(ctx, client, machines)
	if err != nil {
		return nil, err
	}

	return &ControlPlane{
		TCP:          tcp,
		Cluster:      cluster,
		Machines:     machines,
		infraObjects: infraObjects,
		talosConfigs: talosConfigs,
	}, nil
}

// Logger returns a logger with useful context.
func (c *ControlPlane) Logger() logr.Logger {
	return Log.WithValues("namespace", c.TCP.Namespace, "name", c.TCP.Name, "cluster-name", c.Cluster.Name)
}

// MachineWithDeleteAnnotation returns a machine that has been annotated with DeleteMachineAnnotation key.
func (c *ControlPlane) MachineWithDeleteAnnotation(machines collections.Machines) collections.Machines {
	// See if there are any machines with DeleteMachineAnnotation key.
	annotatedMachines := machines.Filter(collections.HasAnnotationKey(clusterv1.DeleteMachineAnnotation))
	// If there are, return list of annotated machines.
	return annotatedMachines
}

// MachinesNeedingRollout return a list of machines that need to be rolled out.
func (c *ControlPlane) MachinesNeedingRollout() collections.Machines {
	if c.TCP.Spec.RolloutStrategy != nil && c.TCP.Spec.RolloutStrategy.Type == controlplanev1.OnDeleteStrategyType {
		return collections.New()
	}

	// Ignore machines to be deleted.
	machines := c.Machines.Filter(collections.Not(collections.HasDeletionTimestamp))

	// Return machines if they are scheduled for rollout or if with an outdated configuration.
	return machines.AnyFilter(
		// Machines that do not match with TCP config.
		collections.Not(
			collections.And(
				collections.MatchesKubernetesVersion(c.TCP.Spec.Version),
				MatchesTemplateClonedFrom(c.infraObjects, c.TCP),
				MatchesControlPlaneConfig(c.talosConfigs, c.TCP),
			),
		),
	)
}

// getInfraResources fetches the external infrastructure resource for each machine in the collection and returns a map of machine.Name -> infraResource.
func getInfraResources(ctx context.Context, cl client.Client, machines collections.Machines) (map[string]*unstructured.Unstructured, error) {
	result := map[string]*unstructured.Unstructured{}
	for _, m := range machines {
		infraObj, err := external.Get(ctx, cl, &m.Spec.InfrastructureRef)
		if err != nil {
			if apierrors.IsNotFound(errors.Cause(err)) {
				continue
			}
			return nil, errors.Wrapf(err, "failed to retrieve infra obj for machine %q", m.Name)
		}
		result[m.Name] = infraObj
	}
	return result, nil
}

// getTalosConfigs fetches the TalosConfigs for each machine in the collection and returns a map of machine.Name -> TalosConfig.
func getTalosConfigs(ctx context.Context, cl client.Client, machines collections.Machines) (map[string]*cabptv1.TalosConfig, error) {
	result := map[string]*cabptv1.TalosConfig{}

	for _, m := range machines {
		bootstrapRef := m.Spec.Bootstrap.ConfigRef
		if bootstrapRef == nil {
			continue
		}

		talosconfig := &cabptv1.TalosConfig{}

		err := cl.Get(ctx, client.ObjectKey{
			Namespace: m.Namespace,
			Name:      bootstrapRef.Name,
		}, talosconfig)
		if err != nil {
			if apierrors.IsNotFound(errors.Cause(err)) {
				continue
			}
			return nil, errors.Wrapf(err, "failed to retrieve talosconfig obj for machine %q", m.Name)
		}

		result[m.Name] = talosconfig
	}

	return result, nil
}

// MatchesTemplateClonedFrom returns a filter to find all machines that match a given TCP infra template.
func MatchesTemplateClonedFrom(infraConfigs map[string]*unstructured.Unstructured, tcp *controlplanev1.TalosControlPlane) collections.Func {
	return func(machine *clusterv1.Machine) bool {
		if machine == nil {
			return false
		}
		infraObj, found := infraConfigs[machine.Name]
		if !found {
			// Return true here because failing to get infrastructure machine should not be considered as unmatching.
			return true
		}

		clonedFromName, ok1 := infraObj.GetAnnotations()[clusterv1.TemplateClonedFromNameAnnotation]
		clonedFromGroupKind, ok2 := infraObj.GetAnnotations()[clusterv1.TemplateClonedFromGroupKindAnnotation]
		if !ok1 || !ok2 {
			// All tcp cloned infra machines should have this annotation.
			// Missing the annotation may be due to older version machines or adopted machines.
			// Should not be considered as mismatch.
			return true
		}

		// Check if the machine's infrastructure reference has been created from the current TCP infrastructure template.
		if clonedFromName != tcp.Spec.InfrastructureTemplate.Name ||
			clonedFromGroupKind != tcp.Spec.InfrastructureTemplate.GroupVersionKind().GroupKind().String() {
			return false
		}

		return true
	}
}

// MatchesControlPlaneConfig returns a filter to find all machines that match a given controlPaneConfig.
func MatchesControlPlaneConfig(talosConfigs map[string]*cabptv1.TalosConfig, tcp *controlplanev1.TalosControlPlane) collections.Func {
	return func(machine *clusterv1.Machine) bool {
		if machine == nil {
			return false
		}

		talosConfig, found := talosConfigs[machine.Name]
		if !found {
			// Return true here because failing to get talosconfig should not be considered as unmatching.
			return true
		}

		return reflect.DeepEqual(tcp.Spec.ControlPlaneConfig.ControlPlaneConfig, talosConfig.Spec)
	}
}
