// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package capi

import (
	"context"
	"fmt"
	"slices"

	"github.com/siderolabs/go-retry/retry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
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

	// under the CAPI v1beta2 contract, Cluster no longer sets a "Ready" condition;
	// "Available" is the equivalent top-level aggregate condition.
	if ready, err = hasTrueCondition(conditions, clusterv1.ReadyCondition, clusterv1.AvailableCondition); err != nil {
		return err
	}

	if !ready {
		return retry.ExpectedError(fmt.Errorf("cluster is not ready"))
	}

	// v1beta2 contract: the Cluster mirrors the control plane initialization and availability
	// in its ControlPlaneInitialized and ControlPlaneAvailable conditions.
	for _, conditionType := range []string{clusterv1.ClusterControlPlaneInitializedCondition, clusterv1.ClusterControlPlaneAvailableCondition} {
		if ready, err = hasTrueCondition(conditions, conditionType); err != nil {
			return err
		}

		if !ready {
			return retry.ExpectedError(fmt.Errorf("cluster condition %s is not True", conditionType))
		}
	}

	controlPlane, err := cluster.ControlPlanes(ctx)
	if err != nil {
		return err
	}

	// v1beta2 contract: the control plane reports its availability in the Available condition,
	// the top-level status.ready and status.initialized fields only exist in the legacy API versions.
	if conditions, found, err = unstructured.NestedSlice(controlPlane.Object, "status", "conditions"); err != nil {
		return err
	}

	if !found {
		return retry.ExpectedError(fmt.Errorf("control plane is not ready"))
	}

	if ready, err = hasTrueCondition(conditions, clusterv1.AvailableCondition); err != nil {
		return err
	}

	if !ready {
		return retry.ExpectedError(fmt.Errorf("control plane is not ready"))
	}

	if initialized, found, err = unstructured.NestedBool(controlPlane.Object, "status", "initialization", "controlPlaneInitialized"); err != nil {
		return err
	}

	if !initialized || !found {
		return retry.ExpectedError(fmt.Errorf("control plane status.initialization.controlPlaneInitialized is not true"))
	}

	if err = checkReplicasReady(*controlPlane); err != nil {
		return err
	}

	if err = checkReplicasUpToDate(*controlPlane); err != nil {
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

// checkReplicasUpToDate verifies the v1beta2 contract replica counters: the available and
// up-to-date replicas must match the desired replicas.
func checkReplicasUpToDate(in unstructured.Unstructured) error {
	object := in.Object

	desiredReplicas, found, err := unstructured.NestedInt64(object, "spec", "replicas")
	if err != nil {
		return err
	}

	if !found {
		return retry.ExpectedError(fieldNotFound("spec", "replicas"))
	}

	for _, field := range []string{"replicas", "availableReplicas", "upToDateReplicas"} {
		replicas, found, err := unstructured.NestedInt64(object, "status", field)
		if err != nil {
			return err
		}

		if !found {
			return retry.ExpectedError(fieldNotFound("status", field))
		}

		if replicas != desiredReplicas {
			return retry.ExpectedError(fmt.Errorf("%s %s desired replicas %d != %s %d", in.GetKind(), in.GetName(), desiredReplicas, field, replicas))
		}
	}

	return nil
}

// hasTrueCondition returns true if any of the given condition types is present with status True.
func hasTrueCondition(conditions []any, conditionTypes ...string) (bool, error) {
	for _, cond := range conditions {
		c, ok := cond.(map[string]any)
		if !ok {
			return false, fmt.Errorf("failed to convert condition to map[string]interface{}")
		}

		t, found, err := unstructured.NestedString(c, "type")
		if err != nil {
			return false, err
		} else if !found {
			return false, fieldNotFound("type")
		}

		status, found, err := unstructured.NestedString(c, "status")
		if err != nil {
			return false, err
		} else if !found {
			return false, fieldNotFound("status")
		}

		if slices.Contains(conditionTypes, t) && corev1.ConditionStatus(status) == corev1.ConditionTrue {
			return true, nil
		}
	}

	return false, nil
}
