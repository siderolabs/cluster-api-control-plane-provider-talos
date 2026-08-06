// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package capi

import (
	"context"
	"fmt"

	"github.com/siderolabs/go-retry/retry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// CheckClusterReady verifies that cluster ready from the CAPI point of view.
//
//nolint:cyclop,gocyclo,gocognit
func (clusterAPI *Manager) CheckClusterReady(ctx context.Context, cluster *Cluster) error {
	var (
		initialized bool
		ready       bool
		found       bool
		conditions  []any
		err         error
	)
	if err = cluster.sync(ctx); err != nil {
		return err
	}

	if conditions, found, err = unstructured.NestedSlice(cluster.cluster.Object, "status", "conditions"); err != nil {
		return err
	}

	if !found {
		return retry.ExpectedError(fmt.Errorf("cluster status is unknown"))
	}

	for _, cond := range conditions {
		var (
			t      string
			status string
		)

		c, ok := cond.(map[string]any)
		if !ok {
			return fmt.Errorf("failed to convert condition to map[string]interface{}")
		}

		if t, found, err = unstructured.NestedString(c, "type"); err != nil {
			return err
		} else if !found {
			return fieldNotFound("type")
		}

		if status, found, err = unstructured.NestedString(c, "status"); err != nil {
			return err
		} else if !found {
			return fieldNotFound("status")
		}

		if clusterv1.ConditionType(t) == clusterv1.ReadyCondition && corev1.ConditionStatus(status) == corev1.ConditionTrue {
			ready = true

			break
		}
	}

	if !ready {
		return retry.ExpectedError(fmt.Errorf("cluster is not ready"))
	}

	controlPlane, err := cluster.ControlPlanes(ctx)
	if err != nil {
		return err
	}

	if ready, found, err = unstructured.NestedBool(controlPlane.Object, "status", "ready"); err != nil {
		return err
	}

	if !ready || !found {
		return retry.ExpectedError(fmt.Errorf("control plane is not ready"))
	}

	if initialized, found, err = unstructured.NestedBool(controlPlane.Object, "status", "initialized"); err != nil {
		return err
	}

	if !initialized || !found {
		return retry.ExpectedError(fmt.Errorf("control plane is not ready"))
	}

	if err = checkReplicasReady(*controlPlane); err != nil {
		return err
	}

	machineDeployments, err := cluster.Workers(ctx)
	if err != nil {
		return err
	}

	for _, machineDeployment := range machineDeployments.Items {
		var phase string

		if phase, found, err = unstructured.NestedString(machineDeployment.Object, "status", "phase"); err != nil {
			return err
		} else if !found {
			return fieldNotFound("status", "phase")
		}

		if clusterv1.MachineDeploymentPhase(phase) != clusterv1.MachineDeploymentPhaseRunning {
			return retry.ExpectedError(fmt.Errorf("machineDeployment phase is %s", phase))
		}

		if err = checkReplicasReady(machineDeployment); err != nil {
			return err
		}
	}

	return nil
}

func checkReplicasReady(in unstructured.Unstructured) error {
	object := in.Object

	readyReplicas, found, err := unstructured.NestedInt64(object, "status", "readyReplicas")
	if err != nil {
		return err
	}

	if !found {
		return retry.ExpectedError(fieldNotFound("status", "readyReplicas"))
	}

	expectedReplicas, found, err := unstructured.NestedInt64(object, "status", "replicas")
	if err != nil {
		return err
	}

	if !found {
		return retry.ExpectedError(fieldNotFound("status", "replicas"))
	}

	if readyReplicas != expectedReplicas {
		return retry.ExpectedError(fmt.Errorf("%s %s replicas %d != ready replicas %d", in.GetKind(), in.GetName(), expectedReplicas, readyReplicas))
	}

	return nil
}
