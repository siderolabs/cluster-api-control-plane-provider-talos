// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package capi

import (
	"context"
	"fmt"
	"time"

	"github.com/siderolabs/go-retry/retry"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// NodeGroup defines CAPI cluster node type group.
type NodeGroup int

const (
	// ControlPlaneNodes control plane nodes group.
	ControlPlaneNodes NodeGroup = iota
	// WorkerNodes worker nodes group.
	WorkerNodes
)

// ScaleOptions defines additional optional parameters for scale method.
type ScaleOptions struct {
	MachineDeploymentName string
}

// ScaleOption optional scale parameter setter.
type ScaleOption func(*ScaleOptions)

// MachineDeploymentName allows setting machine deployment name for scaling
// clusters that have more than one machine group.
func MachineDeploymentName(name string) ScaleOption {
	return func(opts *ScaleOptions) {
		opts.MachineDeploymentName = name
	}
}

// Scale cluster nodes.
//
//nolint:gocognit,gocyclo,cyclop
func (cluster *Cluster) Scale(ctx context.Context, replicas int, nodes NodeGroup, setters ...ScaleOption) error {
	var object *unstructured.Unstructured

	var opts ScaleOptions

	for _, s := range setters {
		s(&opts)
	}

	switch nodes {
	case ControlPlaneNodes:
		controlPlane, err := cluster.ControlPlanes(ctx)
		if err != nil {
			return err
		}

		object = controlPlane
	case WorkerNodes:
		machineDeployments, err := cluster.Workers(ctx)
		if err != nil {
			return err
		}

		if len(machineDeployments.Items) == 0 {
			return fmt.Errorf("cluster has no machine deployments")
		}

		var deployment *unstructured.Unstructured

		if len(machineDeployments.Items) > 1 {
			if opts.MachineDeploymentName == "" {
				return fmt.Errorf("cluster has several machine deployments, please provide MachineDeploymentName")
			}

			for i, d := range machineDeployments.Items {
				if d.GetName() == opts.MachineDeploymentName {
					deployment = &machineDeployments.Items[i]

					break
				}
			}
		} else {
			deployment = &machineDeployments.Items[0]
		}

		object = deployment
	default:
		return fmt.Errorf("unknown nodes group %d", nodes)
	}

	spec, _, err := unstructured.NestedMap(object.Object, "spec")
	if err != nil {
		return err
	}

	// nothing to do
	if spec["replicas"] == replicas {
		return nil
	}

	spec["replicas"] = replicas
	object.Object["spec"] = spec

	if err = cluster.manager.runtimeClient.Update(ctx, object); err != nil {
		return err
	}

	// unstarted scale up/down may look like completed one
	// and cluster health check will pass immediately
	// so wait a bit until it actually starts scaling
	time.Sleep(2 * time.Second)

	err = retry.Constant(30*time.Minute, retry.WithUnits(10*time.Second), retry.WithErrorLogging(true)).Retry(func() error {
		if e := cluster.manager.runtimeClient.Get(ctx, types.NamespacedName{Name: object.GetName(), Namespace: object.GetNamespace()}, object); e != nil {
			return e
		}

		if c := getReplicas(object, "replicas"); c != int64(replicas) {
			return retry.ExpectedErrorf("expected %d, current replicas count: %d", replicas, c)
		}

		if e := cluster.manager.CheckClusterReady(ctx, cluster); e != nil {
			return e
		}

		if e := cluster.Sync(ctx); e != nil {
			return e
		}

		var actualReplicas int

		switch nodes {
		case ControlPlaneNodes:
			actualReplicas = len(cluster.controlPlaneNodes)
		case WorkerNodes:
			actualReplicas = len(cluster.workerNodes)
		}

		if actualReplicas != replicas {
			return retry.ExpectedErrorf("get nodes expected %d, current nodes count: %d", replicas, actualReplicas)
		}

		return nil
	})
	if err != nil {
		return err
	}

	return cluster.Sync(ctx)
}

func getReplicas(object *unstructured.Unstructured, key string) int64 {
	value, ok, e := unstructured.NestedInt64(object.Object, "status", key)
	if e != nil {
		return 0
	}

	if !ok {
		return 0
	}

	return value
}
