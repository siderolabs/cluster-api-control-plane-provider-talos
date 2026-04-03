// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/yaml"
)

func TestTalosControlPlaneDefaultFromMachineTemplate(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: TalosControlPlaneSpec{
			Version: "1.31.0",
			MachineTemplate: TalosControlPlaneMachineTemplate{
				InfrastructureRef: corev1.ObjectReference{
					Name:       "cp-template",
					Kind:       "DockerMachineTemplate",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
			},
		},
	}

	if err := tcp.Default(context.Background(), tcp); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	if got := tcp.Spec.GetReplicas(); got != 1 {
		t.Fatalf("expected replicas to default to 1, got %d", got)
	}
	if got := tcp.Spec.Version; got != "v1.31.0" {
		t.Fatalf("expected version default with v prefix, got %s", got)
	}
	if got := tcp.Spec.MachineTemplate.InfrastructureRef.Namespace; got != "default" {
		t.Fatalf("expected machineTemplate.infrastructureRef namespace default to object namespace, got %s", got)
	}
	if got := tcp.Spec.InfrastructureTemplate.Namespace; got != "default" {
		t.Fatalf("expected legacy infrastructureTemplate namespace default to object namespace, got %s", got)
	}
	if tcp.Spec.InfrastructureTemplate.Name != tcp.Spec.MachineTemplate.InfrastructureRef.Name {
		t.Fatalf("expected legacy infrastructureTemplate to stay in sync with machineTemplate.infrastructureRef")
	}
	if tcp.Spec.RolloutStrategy == nil || tcp.Spec.RolloutStrategy.RollingUpdate == nil || tcp.Spec.RolloutStrategy.RollingUpdate.MaxSurge == nil {
		t.Fatalf("expected rollout strategy defaults to be set")
	}
	if got := tcp.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue(); got != 1 {
		t.Fatalf("expected maxSurge default to 1, got %d", got)
	}
}

func TestTalosControlPlaneDefaultFromLegacyInfrastructureTemplate(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: TalosControlPlaneSpec{
			Version: "1.31.0",
			InfrastructureTemplate: corev1.ObjectReference{
				Name:       "cp-template",
				Kind:       "DockerMachineTemplate",
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}

	if err := tcp.Default(context.Background(), tcp); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	if got := tcp.Spec.MachineTemplate.InfrastructureRef.Namespace; got != "default" {
		t.Fatalf("expected machineTemplate.infrastructureRef namespace default to object namespace, got %s", got)
	}
	if tcp.Spec.MachineTemplate.InfrastructureRef.Name != tcp.Spec.InfrastructureTemplate.Name {
		t.Fatalf("expected machineTemplate.infrastructureRef to be backfilled from legacy infrastructureTemplate")
	}
}

func TestTalosControlPlaneValidateCreate(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		Spec: TalosControlPlaneSpec{
			Version: "v1.31.0",
			MachineTemplate: TalosControlPlaneMachineTemplate{
				InfrastructureRef: corev1.ObjectReference{
					Name:       "cp-template",
					Kind:       "DockerMachineTemplate",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
			},
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

func TestTalosControlPlaneValidateCreateRejectsMissingInfrastructureRef(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		Spec: TalosControlPlaneSpec{
			Version: "v1.31.0",
		},
	}

	_, err := tcp.ValidateCreate(context.Background(), tcp)
	if err == nil {
		t.Fatal("expected validation error when no infrastructure reference is set")
	}
}

func TestTalosControlPlaneValidateCreateRejectsConflictingInfrastructureRefs(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		Spec: TalosControlPlaneSpec{
			Version: "v1.31.0",
			MachineTemplate: TalosControlPlaneMachineTemplate{
				InfrastructureRef: corev1.ObjectReference{
					Name:       "cp-template-a",
					Kind:       "DockerMachineTemplate",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
			},
			InfrastructureTemplate: corev1.ObjectReference{
				Name:       "cp-template-b",
				Kind:       "DockerMachineTemplate",
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}

	_, err := tcp.ValidateCreate(context.Background(), tcp)
	if err == nil {
		t.Fatal("expected validation error when infrastructure references conflict")
	}
}

func TestTalosControlPlaneValidateCreateRejectsInvalidMachineNamingStrategy(t *testing.T) {
	t.Parallel()

	tcp := &TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp",
		},
		Spec: TalosControlPlaneSpec{
			Version: "v1.31.0",
			MachineTemplate: TalosControlPlaneMachineTemplate{
				InfrastructureRef: corev1.ObjectReference{
					Name:       "cp-template",
					Kind:       "DockerMachineTemplate",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
			},
			MachineNamingStrategy: &MachineNamingStrategy{
				Template: "{{ .talosControlPlane.name }}",
			},
		},
	}

	_, err := tcp.ValidateCreate(context.Background(), tcp)
	if err == nil {
		t.Fatal("expected validation error for machine naming strategy without {{ .random }}")
	}
}

func TestTalosControlPlaneTemplateDefault(t *testing.T) {
	t.Parallel()

	tcpt := &TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Metadata: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": "workload-cluster",
					},
				},
				Spec: TalosControlPlaneTemplateResourceSpec{
					MachineTemplate: TalosControlPlaneMachineTemplate{
						InfrastructureRef: corev1.ObjectReference{
							Name:       "cp-template",
							Kind:       "DockerMachineTemplate",
							APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
						},
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

	if err := tcpt.Default(context.Background(), tcpt); err != nil {
		t.Fatalf("default failed: %v", err)
	}

	if got := tcpt.Spec.Template.Spec.MachineTemplate.InfrastructureRef.Namespace; got != "default" {
		t.Fatalf("expected machineTemplate.infrastructureRef namespace default to object namespace, got %s", got)
	}
	if tcpt.Spec.Template.Spec.InfrastructureTemplate.Name != tcpt.Spec.Template.Spec.MachineTemplate.InfrastructureRef.Name {
		t.Fatalf("expected legacy infrastructureTemplate to stay in sync with machineTemplate.infrastructureRef")
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

func TestTalosControlPlaneTemplateValidateCreateRejectsInvalidMachineNamingStrategy(t *testing.T) {
	t.Parallel()

	tcpt := &TalosControlPlaneTemplate{
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					MachineNamingStrategy: &MachineNamingStrategy{
						Template: "{{ .talosControlPlane.name }}",
					},
				},
			},
		},
	}

	_, err := tcpt.ValidateCreate(context.Background(), tcpt)
	if err == nil {
		t.Fatal("expected validation error for template machine naming strategy without {{ .random }}")
	}
}

func TestTalosControlPlaneTemplateAllowsClusterClassMachineInfrastructure(t *testing.T) {
	t.Parallel()

	template := &TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					MachineTemplate: TalosControlPlaneMachineTemplate{
						Metadata: clusterv1.ObjectMeta{
							Labels: map[string]string{
								"example.siderolabs.dev/control-plane": "true",
							},
						},
						ReadinessGates: []clusterv1.MachineReadinessGate{
							{ConditionType: "APIServerReady"},
						},
					},
					ControlPlaneConfig: ControlPlaneConfig{
						ControlPlaneConfig: cabptControlPlaneConfig("controlplane", []string{"machine:\n  install:\n    disk: /dev/sda\n"}),
					},
				},
			},
		},
	}

	if err := template.Default(context.Background(), template); err != nil {
		t.Fatalf("default failed: %v", err)
	}
	if _, err := template.ValidateCreate(context.Background(), template); err != nil {
		t.Fatalf("expected template without infrastructureRef to be allowed for ClusterClass machineInfrastructure flow: %v", err)
	}

	data, err := json.Marshal(template.Spec.Template.Spec)
	if err != nil {
		t.Fatalf("failed to marshal template spec: %v", err)
	}

	replicas := int32(3)
	generated := TalosControlPlaneSpec{
		Replicas: &replicas,
		Version:  "v1.31.0",
	}
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatalf("failed to unmarshal template spec into TalosControlPlaneSpec: %v", err)
	}

	// Simulate the topology controller injecting ClusterClass.spec.controlPlane.machineInfrastructure.ref.
	generated.MachineTemplate.InfrastructureRef = corev1.ObjectReference{
		Name:       "cp-template",
		Kind:       "DockerMachineTemplate",
		APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
		Namespace:  "default",
	}

	tcp := &TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: generated,
	}

	if err := tcp.Default(context.Background(), tcp); err != nil {
		t.Fatalf("default failed: %v", err)
	}
	if _, err := tcp.ValidateCreate(context.Background(), tcp); err != nil {
		t.Fatalf("expected generated TalosControlPlane to validate after machineInfrastructure injection: %v", err)
	}
	if tcp.Spec.InfrastructureTemplate.Name != "cp-template" {
		t.Fatalf("expected legacy infrastructureTemplate to be synchronized from machineTemplate.infrastructureRef")
	}
	if !reflect.DeepEqual(tcp.Spec.MachineTemplate.Metadata, template.Spec.Template.Spec.MachineTemplate.Metadata) {
		t.Fatalf("expected machineTemplate metadata to survive template-to-spec conversion")
	}
	if !reflect.DeepEqual(tcp.Spec.ControlPlaneConfig, template.Spec.Template.Spec.ControlPlaneConfig) {
		t.Fatalf("control plane config mismatch after conversion")
	}
}

func TestStrategicPatchesMustBeStrings(t *testing.T) {
	t.Parallel()

	goodYAML := []byte(`
machineTemplate:
  metadata:
    labels:
      example.siderolabs.dev/control-plane: "true"
controlPlaneConfig:
  controlplane:
    generateType: controlplane
    strategicPatches:
      - |
        machine:
          install:
            disk: /dev/sda
`)

	var good TalosControlPlaneTemplateResourceSpec
	if err := yaml.Unmarshal(goodYAML, &good); err != nil {
		t.Fatalf("expected strategicPatches block scalars to unmarshal: %v", err)
	}
	if len(good.ControlPlaneConfig.ControlPlaneConfig.StrategicPatches) != 1 {
		t.Fatalf("expected one strategic patch, got %d", len(good.ControlPlaneConfig.ControlPlaneConfig.StrategicPatches))
	}

	badYAML := []byte(`
controlPlaneConfig:
  controlplane:
    generateType: controlplane
    strategicPatches:
      - machine:
          install:
            disk: /dev/sda
`)

	var bad TalosControlPlaneTemplateResourceSpec
	if err := yaml.Unmarshal(badYAML, &bad); err == nil {
		t.Fatal("expected strategicPatches object entries to fail because the schema requires strings")
	}
}

func cabptControlPlaneConfig(generateType string, strategicPatches []string) cabptv1.TalosConfigSpec {
	return cabptv1.TalosConfigSpec{
		GenerateType:     generateType,
		StrategicPatches: strategicPatches,
	}
}
