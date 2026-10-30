// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"reflect"
	"slices"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/failuredomains"
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
	infraObjects, err := getInfraResources(ctx, client, machines, cluster.Namespace)
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

// UnhealthyMachines returns the machines a MachineHealthCheck flagged as unhealthy and left to the control plane to
// remediate that are not being deleted yet.
func (c *ControlPlane) UnhealthyMachines() collections.Machines {
	return c.Machines.Filter(collections.IsUnhealthyAndOwnerRemediated, collections.Not(collections.HasDeletionTimestamp))
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
		collections.Not(c.MatchesSpec()),
	)
}

// MatchesSpec returns a filter to find all machines that match the current TCP spec:
// Kubernetes version, infrastructure template and controlplane TalosConfig.
//
// It is the predicate used both to pick machines for rollout and to report the Machine UpToDate condition.
func (c *ControlPlane) MatchesSpec() collections.Func {
	return collections.And(
		collections.MatchesKubernetesVersion(c.TCP.Spec.Version),
		MatchesTemplateClonedFrom(c.infraObjects, c.TCP),
		MatchesControlPlaneConfig(c.talosConfigs, c.TCP),
	)
}

// UpToDateMachines returns the machines which do not need to be rolled out.
func (c *ControlPlane) UpToDateMachines() collections.Machines {
	return c.Machines.Difference(c.MachinesNeedingRollout())
}

// FailureDomains returns the failure domains the control plane machines can be placed in.
//
// Only the domains explicitly marked for the control plane are returned. If the infrastructure
// provider marks none of them, all the cluster failure domains are returned instead: unlike the
// KubeadmControlPlane, which filters strictly, falling back keeps the spreading working with
// providers which leave the flag unset.
func (c *ControlPlane) FailureDomains() []clusterv1.FailureDomain {
	if len(c.Cluster.Status.FailureDomains) == 0 {
		return nil
	}

	var controlPlaneDomains []clusterv1.FailureDomain

	for _, failureDomain := range c.Cluster.Status.FailureDomains {
		if ptr.Deref(failureDomain.ControlPlane, false) {
			controlPlaneDomains = append(controlPlaneDomains, failureDomain)
		}
	}

	if len(controlPlaneDomains) > 0 {
		return controlPlaneDomains
	}

	return c.Cluster.Status.FailureDomains
}

// NextFailureDomainForScaleUp returns the failure domain to place a new control plane machine in,
// which is the one with the fewest up-to-date, not deleted machines.
//
// Spreading the up-to-date machines takes priority, so that the machines end up evenly spread once
// a rollout completes. Ties are broken by the fewest number of machines overall, which keeps the
// machines spread while the rollout is still in progress.
//
// Returns an empty string if the cluster has no failure domain to place the machine in.
func (c *ControlPlane) NextFailureDomainForScaleUp(ctx context.Context) string {
	failureDomains := c.FailureDomains()
	if len(failureDomains) == 0 {
		return ""
	}

	upToDateMachines := c.UpToDateMachines().Filter(collections.Not(collections.HasDeletionTimestamp))

	return failuredomains.PickFewest(ctx, failureDomains, c.Machines, upToDateMachines)
}

// FailureDomainWithMostMachines returns the failure domain to remove a control plane machine from,
// which is the one with the most machines and at least one eligible machine in it.
//
// Machines which are not in one of the current failure domains take precedence: getting rid of a
// machine placed in a domain which is gone from the cluster status is more valuable than balancing
// the remaining ones.
func (c *ControlPlane) FailureDomainWithMostMachines(ctx context.Context, eligibleMachines collections.Machines) string {
	failureDomains := c.FailureDomains()

	notInFailureDomains := eligibleMachines.Filter(
		collections.Not(collections.InFailureDomains(failureDomainNames(failureDomains)...)),
	)

	if len(notInFailureDomains) > 0 {
		// return the failure domain of the oldest machine, which may well be empty for a machine
		// which never had one assigned.
		return notInFailureDomains.Oldest().Spec.FailureDomain
	}

	return failuredomains.PickMost(ctx, failureDomains, c.Machines, eligibleMachines)
}

// failureDomainNames returns the names of the given failure domains, sorted.
func failureDomainNames(failureDomains []clusterv1.FailureDomain) []string {
	names := make([]string, 0, len(failureDomains))

	for _, failureDomain := range failureDomains {
		names = append(names, failureDomain.Name)
	}

	slices.Sort(names)

	return names
}

// MachineInFailureDomainWithMostMachines returns the oldest eligible machine in the failure domain
// holding the most control plane machines.
//
// Removing machines from the most crowded domain is what keeps the control plane spread across
// failure domains during a rollout, where every scale up is followed by a scale down.
func (c *ControlPlane) MachineInFailureDomainWithMostMachines(ctx context.Context, eligibleMachines collections.Machines) (*clusterv1.Machine, error) {
	failureDomain := c.FailureDomainWithMostMachines(ctx, eligibleMachines)

	machineToDelete := eligibleMachines.Filter(collections.InFailureDomains(failureDomain)).Oldest()
	if machineToDelete == nil {
		return nil, errors.New("failed to pick a control plane machine to delete")
	}

	return machineToDelete, nil
}

// getInfraResources fetches the external infrastructure resource for each machine in the collection and returns a map of machine.Name -> infraResource.
func getInfraResources(ctx context.Context, cl client.Client, machines collections.Machines, namespace string) (map[string]*unstructured.Unstructured, error) {
	result := map[string]*unstructured.Unstructured{}
	for _, m := range machines {
		infraObj, err := external.GetObjectFromContractVersionedRef(ctx, cl, m.Spec.InfrastructureRef, namespace)
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
		if !bootstrapRef.IsDefined() {
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
		templateRef := tcp.Spec.MachineTemplate.Spec.InfrastructureRef
		templateGroupKind := schema.GroupKind{Group: templateRef.APIGroup, Kind: templateRef.Kind}.String()

		if clonedFromName != templateRef.Name || clonedFromGroupKind != templateGroupKind {
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
