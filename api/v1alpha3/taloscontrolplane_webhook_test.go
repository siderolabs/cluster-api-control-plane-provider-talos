// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func TestTalosControlPlaneDefault(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		Spec: TalosControlPlaneSpec{
			Version: "1.31.0",
			InfrastructureTemplate: corev1.ObjectReference{
				Name:       "cp-template",
				Kind:       "DockerMachineTemplate",
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	tcp.Namespace = "default"

	if err := tcp.Default(context.Background(), tcp); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	if got := tcp.Spec.GetReplicas(); got != 1 {
		t.Fatalf("expected replicas to default to 1, got %d", got)
	}
	if got := tcp.Spec.Version; got != "v1.31.0" {
		t.Fatalf("expected version default with v prefix, got %s", got)
	}
	if got := tcp.Spec.InfrastructureTemplate.Namespace; got != "default" {
		t.Fatalf("expected infra namespace default to object namespace, got %s", got)
	}
	if tcp.Spec.RolloutStrategy == nil || tcp.Spec.RolloutStrategy.RollingUpdate == nil || tcp.Spec.RolloutStrategy.RollingUpdate.MaxSurge == nil {
		t.Fatalf("expected rollout strategy defaults to be set")
	}
	if got := tcp.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue(); got != 1 {
		t.Fatalf("expected maxSurge default to 1, got %d", got)
	}
}

func TestTalosControlPlaneValidateCreate(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		Spec: TalosControlPlaneSpec{
			Version: "v1.31.0",
			RolloutStrategy: &RolloutStrategy{
				Type: RolloutStrategyType("Invalid"),
			},
		},
	}

	_, err := tcp.ValidateCreate(context.Background(), tcp)
	if err == nil {
		t.Fatal("expected validation error for invalid rollout strategy type")
	}
}

func TestTalosControlPlaneTemplateDefault(t *testing.T) {
	t.Parallel()

	tcpt := &TalosControlPlaneTemplate{
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Metadata: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": "workload-cluster",
					},
				},
				Spec: TalosControlPlaneTemplateResourceSpec{
					InfrastructureTemplate: corev1.ObjectReference{
						Name:       "cp-template",
						Kind:       "DockerMachineTemplate",
						APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
					},
					RolloutStrategy: &RolloutStrategy{
						Type: RollingUpdateStrategyType,
						RollingUpdate: &RollingUpdate{
							MaxSurge: func() *intstr.IntOrString {
								v := intstr.FromInt(2)
								return &v
							}(),
						},
					},
				},
			},
		},
	}
	tcpt.Namespace = "default"

	if err := tcpt.Default(context.Background(), tcpt); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	if got := tcpt.Spec.Template.Spec.InfrastructureTemplate.Namespace; got != "default" {
		t.Fatalf("expected infra namespace default to object namespace, got %s", got)
	}
	if got := tcpt.Spec.Template.Metadata.Labels["cluster.x-k8s.io/cluster-name"]; got != "workload-cluster" {
		t.Fatalf("expected template metadata labels to be preserved, got %q", got)
	}
	if got := tcpt.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue(); got != 2 {
		t.Fatalf("expected user-provided maxSurge to be preserved, got %d", got)
	}
}

func TestTalosControlPlaneTemplateValidateCreate(t *testing.T) {
	t.Parallel()

	tcpt := &TalosControlPlaneTemplate{
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					RolloutStrategy: &RolloutStrategy{Type: RolloutStrategyType("Invalid")},
				},
			},
		},
	}

	_, err := tcpt.ValidateCreate(context.Background(), tcpt)
	if err == nil {
		t.Fatal("expected validation error for invalid rollout strategy type")
	}
}

func TestTalosControlPlaneTemplateValidateUpdate(t *testing.T) {
	t.Parallel()

	oldObj := &TalosControlPlaneTemplate{}
	newObj := &TalosControlPlaneTemplate{
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					RolloutStrategy: &RolloutStrategy{Type: RolloutStrategyType("Invalid")},
				},
			},
		},
	}

	_, err := newObj.ValidateUpdate(context.Background(), oldObj, newObj)
	if err == nil {
		t.Fatal("expected validation error for invalid rollout strategy type on update")
	}
}

func TestTalosControlPlaneTemplateTopologySpecCompatibility(t *testing.T) {
	t.Parallel()

	replicas := int32(3)
	template := &TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Metadata: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": "workload-cluster",
					},
					Annotations: map[string]string{
						"topology.cluster.x-k8s.io/owned": "",
					},
				},
				Spec: TalosControlPlaneTemplateResourceSpec{
					InfrastructureTemplate: corev1.ObjectReference{
						Name:       "cp-template",
						Kind:       "DockerMachineTemplate",
						APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
					},
				},
			},
		},
	}

	if err := template.Default(context.Background(), template); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	data, err := json.Marshal(template.Spec.Template.Spec)
	if err != nil {
		t.Fatalf("failed to marshal template spec: %v", err)
	}

	generated := TalosControlPlaneSpec{
		Replicas: &replicas,
		Version:  "v1.31.0",
	}

	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("failed to unmarshal template spec into TalosControlPlaneSpec: %v", err)
	}

	if generated.InfrastructureTemplate.Name != template.Spec.Template.Spec.InfrastructureTemplate.Name {
		t.Fatalf("infrastructure template name mismatch: got %q, want %q", generated.InfrastructureTemplate.Name, template.Spec.Template.Spec.InfrastructureTemplate.Name)
	}
	if !reflect.DeepEqual(generated.ControlPlaneConfig, template.Spec.Template.Spec.ControlPlaneConfig) {
		t.Fatalf("control plane config mismatch after conversion")
	}
	if generated.RolloutStrategy == nil || generated.RolloutStrategy.Type != RollingUpdateStrategyType {
		t.Fatalf("expected rollout strategy to remain RollingUpdate after conversion")
	}
}
