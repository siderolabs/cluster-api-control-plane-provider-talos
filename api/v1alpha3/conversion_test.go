// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	cpv1beta1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

func TestTalosControlPlaneListConversionRemapsInfrastructureRef(t *testing.T) {
	SetAPIVersionGetter(func(gk schema.GroupKind) (string, error) {
		return gk.Group + "/v1beta2", nil
	})

	infraTemplate := corev1.ObjectReference{
		APIVersion: "infrastructure.cluster.x-k8s.io/v1beta2",
		Kind:       "GenericInfrastructureMachineTemplate",
		Namespace:  "default",
		Name:       "infra",
	}

	src := &TalosControlPlaneList{
		Items: []TalosControlPlane{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
				Spec:       TalosControlPlaneSpec{InfrastructureTemplate: infraTemplate},
			},
		},
	}

	hub := &cpv1beta1.TalosControlPlaneList{}
	if err := src.ConvertTo(hub); err != nil {
		t.Fatalf("ConvertTo failed: %v", err)
	}

	if len(hub.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(hub.Items))
	}

	infraRef := hub.Items[0].Spec.MachineTemplate.Spec.InfrastructureRef
	if infraRef.APIGroup != "infrastructure.cluster.x-k8s.io" || infraRef.Kind != infraTemplate.Kind || infraRef.Name != infraTemplate.Name {
		t.Fatalf("unexpected infrastructureRef after ConvertTo: %+v", infraRef)
	}

	dst := &TalosControlPlaneList{}
	if err := dst.ConvertFrom(hub); err != nil {
		t.Fatalf("ConvertFrom failed: %v", err)
	}

	if len(dst.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(dst.Items))
	}

	if dst.Items[0].Spec.InfrastructureTemplate != infraTemplate {
		t.Fatalf("unexpected infrastructureTemplate after ConvertFrom: %+v", dst.Items[0].Spec.InfrastructureTemplate)
	}
}

func TestTalosControlPlaneListConversionToleratesUnresolvableInfrastructureRef(t *testing.T) {
	SetAPIVersionGetter(func(gk schema.GroupKind) (string, error) {
		if gk.Kind == "UninstalledMachineTemplate" {
			return "", errors.New("no matches for kind")
		}

		return gk.Group + "/v1beta2", nil
	})

	infraRef := func(kind string) clusterv1.ContractVersionedObjectReference {
		return clusterv1.ContractVersionedObjectReference{
			APIGroup: "infrastructure.cluster.x-k8s.io",
			Kind:     kind,
			Name:     "infra",
		}
	}

	hubItem := func(name, kind string) cpv1beta1.TalosControlPlane {
		return cpv1beta1.TalosControlPlane{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: cpv1beta1.TalosControlPlaneSpec{
				MachineTemplate: cpv1beta1.TalosControlPlaneMachineTemplate{
					Spec: cpv1beta1.TalosControlPlaneMachineTemplateSpec{InfrastructureRef: infraRef(kind)},
				},
			},
		}
	}

	hub := &cpv1beta1.TalosControlPlaneList{
		Items: []cpv1beta1.TalosControlPlane{
			hubItem("cp-1", "GenericInfrastructureMachineTemplate"),
			hubItem("cp-2", "UninstalledMachineTemplate"),
		},
	}

	spoke := &TalosControlPlaneList{}
	if err := spoke.ConvertFrom(hub); err != nil {
		t.Fatalf("ConvertFrom failed: %v", err)
	}

	if got := spoke.Items[0].Spec.InfrastructureTemplate.APIVersion; got != "infrastructure.cluster.x-k8s.io/v1beta2" {
		t.Fatalf("unexpected apiVersion of the resolvable reference: %q", got)
	}

	unresolved := spoke.Items[1].Spec.InfrastructureTemplate
	if unresolved.APIVersion != "" || unresolved.Kind != "UninstalledMachineTemplate" || unresolved.Name != "infra" || unresolved.Namespace != "default" {
		t.Fatalf("unexpected unresolvable reference after ConvertFrom: %+v", unresolved)
	}

	restored := &cpv1beta1.TalosControlPlaneList{}
	if err := spoke.ConvertTo(restored); err != nil {
		t.Fatalf("ConvertTo failed: %v", err)
	}

	for i := range hub.Items {
		if got, want := restored.Items[i].Spec.MachineTemplate.Spec.InfrastructureRef, hub.Items[i].Spec.MachineTemplate.Spec.InfrastructureRef; got != want {
			t.Fatalf("infrastructureRef did not round-trip: got %+v, want %+v", got, want)
		}
	}
}

func TestTalosControlPlaneConversionKeepsZeroReplicaCounters(t *testing.T) {
	SetAPIVersionGetter(func(gk schema.GroupKind) (string, error) {
		return gk.Group + "/v1beta2", nil
	})

	for _, tt := range []struct {
		name          string
		readyReplicas *int32
	}{
		{name: "zero", readyReplicas: ptr.To[int32](0)},
		{name: "unset"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hub := &cpv1beta1.TalosControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
				Status: cpv1beta1.TalosControlPlaneStatus{
					Replicas:      ptr.To[int32](3),
					ReadyReplicas: tt.readyReplicas,
				},
			}

			spoke := &TalosControlPlane{}
			if err := spoke.ConvertFrom(hub); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}

			if spoke.Status.Replicas != 3 || spoke.Status.ReadyReplicas != 0 {
				t.Fatalf("unexpected replica counters after ConvertFrom: %d/%d", spoke.Status.ReadyReplicas, spoke.Status.Replicas)
			}

			restored := &cpv1beta1.TalosControlPlane{}
			if err := spoke.ConvertTo(restored); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			if !ptr.Equal(restored.Status.Replicas, hub.Status.Replicas) || !ptr.Equal(restored.Status.ReadyReplicas, hub.Status.ReadyReplicas) {
				t.Fatalf("replica counters did not round-trip: replicas=%v readyReplicas=%v", restored.Status.Replicas, restored.Status.ReadyReplicas)
			}
		})
	}
}

// TestTalosControlPlaneConversionOfStoredStatus converts the status a TalosControlPlane was stored with in v1alpha3
// by the earlier releases: v0.5.x stored v1beta1 conditions in status.conditions, v0.6.0-alpha.0 v1beta2 ones.
func TestTalosControlPlaneConversionOfStoredStatus(t *testing.T) {
	SetAPIVersionGetter(func(gk schema.GroupKind) (string, error) {
		return gk.Group + "/v1beta2", nil
	})

	for _, tt := range []struct {
		name   string
		status string
		// wantConditions are the types of the v1beta2 conditions, wantLegacyConditions of the deprecated v1beta1 ones.
		wantConditions       []string
		wantLegacyConditions []string
		wantAvailable        *int32
	}{
		{
			name: "v0.5.x",
			status: `{
				"replicas": 3, "readyReplicas": 3, "initialized": true, "ready": true,
				"conditions": [
					{"type": "Ready", "status": "True", "lastTransitionTime": "2026-09-01T00:00:00Z"},
					{"type": "MachinesReady", "status": "False", "severity": "Warning", "reason": "Unhealthy", "lastTransitionTime": "2026-09-01T00:00:00Z"}
				]
			}`,
			wantLegacyConditions: []string{"Ready", "MachinesReady"},
		},
		{
			name: "v0.6.0-alpha.0",
			status: `{
				"replicas": 3, "readyReplicas": 3, "availableReplicas": 2, "upToDateReplicas": 3,
				"initialized": true, "ready": true, "initialization": {"controlPlaneInitialized": true},
				"conditions": [
					{"type": "Available", "status": "True", "reason": "Available", "lastTransitionTime": "2026-10-29T00:00:00Z"},
					{"type": "MachinesReady", "status": "False", "reason": "NotReady", "message": "1 Machine not ready", "lastTransitionTime": "2026-10-29T00:00:00Z"}
				]
			}`,
			wantConditions: []string{"Available", "MachinesReady"},
			wantAvailable:  ptr.To[int32](2),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spoke := &TalosControlPlane{}
			if err := json.Unmarshal([]byte(`{"metadata": {"name": "cp", "namespace": "default"}, "status": `+tt.status+`}`), spoke); err != nil {
				t.Fatalf("failed to decode the stored TalosControlPlane: %v", err)
			}

			hub := &cpv1beta1.TalosControlPlane{}
			if err := spoke.ConvertTo(hub); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			var conditionTypes []string
			for _, condition := range hub.Status.Conditions {
				conditionTypes = append(conditionTypes, condition.Type)
			}

			var legacyConditionTypes []string
			if hub.Status.Deprecated != nil && hub.Status.Deprecated.V1Beta1 != nil {
				for _, condition := range hub.Status.Deprecated.V1Beta1.Conditions {
					legacyConditionTypes = append(legacyConditionTypes, string(condition.Type))
				}
			}

			if !equalStrings(conditionTypes, tt.wantConditions) {
				t.Fatalf("unexpected v1beta2 conditions: got %v, want %v", conditionTypes, tt.wantConditions)
			}

			if !equalStrings(legacyConditionTypes, tt.wantLegacyConditions) {
				t.Fatalf("unexpected v1beta1 conditions: got %v, want %v", legacyConditionTypes, tt.wantLegacyConditions)
			}

			if !ptr.Equal(hub.Status.AvailableReplicas, tt.wantAvailable) {
				t.Fatalf("unexpected availableReplicas: got %v, want %v", hub.Status.AvailableReplicas, tt.wantAvailable)
			}

			if !ptr.Deref(hub.Status.Initialization.ControlPlaneInitialized, false) {
				t.Fatalf("controlPlaneInitialized is not set")
			}

			for _, condition := range hub.Status.Conditions {
				if condition.Type == "MachinesReady" && (condition.Reason != "NotReady" || condition.Message != "1 Machine not ready") {
					t.Fatalf("condition MachinesReady was not kept: %+v", condition)
				}
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
