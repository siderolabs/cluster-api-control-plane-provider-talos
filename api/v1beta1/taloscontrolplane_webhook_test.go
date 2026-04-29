// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1beta1

import (
	"context"
	"strings"
	"testing"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
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
				Spec: TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Name:     "cp-template",
						Kind:     "DockerMachineTemplate",
						APIGroup: "infrastructure.cluster.x-k8s.io",
					},
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
			MachineTemplate: TalosControlPlaneMachineTemplate{
				Spec: TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Name:     "cp-template",
						Kind:     "DockerMachineTemplate",
						APIGroup: "infrastructure.cluster.x-k8s.io",
					},
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

func TestTalosControlPlaneValidateCreateRejectsIncompleteInfrastructureRef(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ref  clusterv1.ContractVersionedObjectReference
	}{
		{
			name: "missing kind",
			ref: clusterv1.ContractVersionedObjectReference{
				Name:     "cp-template",
				APIGroup: "infrastructure.cluster.x-k8s.io",
			},
		},
		{
			name: "missing apiGroup",
			ref: clusterv1.ContractVersionedObjectReference{
				Name: "cp-template",
				Kind: "DockerMachineTemplate",
			},
		},
		{
			name: "missing name",
			ref: clusterv1.ContractVersionedObjectReference{
				Kind:     "DockerMachineTemplate",
				APIGroup: "infrastructure.cluster.x-k8s.io",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tcp := &TalosControlPlane{
				Spec: TalosControlPlaneSpec{
					Version: "v1.31.0",
					MachineTemplate: TalosControlPlaneMachineTemplate{
						Spec: TalosControlPlaneMachineTemplateSpec{
							InfrastructureRef: tc.ref,
						},
					},
				},
			}

			_, err := tcp.ValidateCreate(context.Background(), tcp)
			if err == nil {
				t.Fatal("expected validation error when infrastructureRef is incomplete")
			}
		})
	}
}

func TestTalosControlPlaneValidateCreateAcceptsRandomTemplateVariants(t *testing.T) {
	t.Parallel()

	for _, tpl := range []string{
		"{{ .talosControlPlane.name }}-{{ .random }}",
		"{{ .talosControlPlane.name }}-{{.random}}",
		"{{ .talosControlPlane.name }}-{{- .random -}}",
		"{{ .talosControlPlane.name }}-{{  .random  }}",
	} {
		t.Run(tpl, func(t *testing.T) {
			t.Parallel()

			tcp := &TalosControlPlane{
				ObjectMeta: metav1.ObjectMeta{Name: "cp"},
				Spec: TalosControlPlaneSpec{
					Version: "v1.31.0",
					MachineTemplate: TalosControlPlaneMachineTemplate{
						Spec: TalosControlPlaneMachineTemplateSpec{
							InfrastructureRef: clusterv1.ContractVersionedObjectReference{
								Name:     "cp-template",
								Kind:     "DockerMachineTemplate",
								APIGroup: "infrastructure.cluster.x-k8s.io",
							},
						},
					},
					MachineNamingStrategy: &MachineNamingStrategy{Template: tpl},
				},
			}

			if _, err := tcp.ValidateCreate(context.Background(), tcp); err != nil {
				t.Fatalf("expected template %q to be accepted, got error: %v", tpl, err)
			}
		})
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
				Spec: TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Name:     "cp-template",
						Kind:     "DockerMachineTemplate",
						APIGroup: "infrastructure.cluster.x-k8s.io",
					},
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
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-name": "workload-cluster",
					},
				},
				Spec: TalosControlPlaneTemplateResourceSpec{
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

	if got := tcpt.Spec.Template.ObjectMeta.Labels["cluster.x-k8s.io/cluster-name"]; got != "workload-cluster" {
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

	one := intstr.FromInt(1)
	two := intstr.FromInt(2)

	oldObj := &TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					ControlPlaneConfig: ControlPlaneConfig{
						ControlPlaneConfig: cabptControlPlaneConfig("controlplane", nil),
					},
					RolloutStrategy: &RolloutStrategy{
						Type:          RollingUpdateStrategyType,
						RollingUpdate: &RollingUpdate{MaxSurge: &one},
					},
				},
			},
		},
	}
	newObj := oldObj.DeepCopy()
	newObj.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge = &two

	_, err := newObj.ValidateUpdate(context.Background(), oldObj, newObj)
	if err == nil {
		t.Fatal("expected validation error for immutable spec.template.spec")
	}
	if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutability error, got: %v", err)
	}
}

func TestTalosControlPlaneTemplateValidateUpdateAllowsMetadataOnlyChanges(t *testing.T) {
	t.Parallel()

	obj := &TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "template",
			Namespace: "default",
		},
		Spec: TalosControlPlaneTemplateSpec{
			Template: TalosControlPlaneTemplateResource{
				Spec: TalosControlPlaneTemplateResourceSpec{
					ControlPlaneConfig: ControlPlaneConfig{
						ControlPlaneConfig: cabptControlPlaneConfig("controlplane", nil),
					},
				},
			},
		},
	}

	updated := obj.DeepCopy()
	updated.Labels = map[string]string{
		"example.siderolabs.dev/revision": "2",
	}

	if _, err := updated.ValidateUpdate(context.Background(), obj, updated); err != nil {
		t.Fatalf("expected metadata-only update to succeed: %v", err)
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
					MachineTemplate: TalosControlPlaneTemplateMachineTemplate{
						ObjectMeta: clusterv1.ObjectMeta{
							Labels: map[string]string{
								"example.siderolabs.dev/control-plane": "true",
							},
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

	// Simulate the topology controller projecting the template's MachineTemplate metadata onto a
	// concrete TalosControlPlane and then injecting ClusterClass.spec.controlPlane.machineInfrastructure
	// into machineTemplate.spec.infrastructureRef.
	tcp := &TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
		},
		Spec: TalosControlPlaneSpec{
			Replicas: func() *int32 { v := int32(3); return &v }(),
			Version:  "v1.31.0",
			MachineTemplate: TalosControlPlaneMachineTemplate{
				ObjectMeta: template.Spec.Template.Spec.MachineTemplate.ObjectMeta,
				Spec: TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						Name:     "cp-template",
						Kind:     "DockerMachineTemplate",
						APIGroup: "infrastructure.cluster.x-k8s.io",
					},
				},
			},
			ControlPlaneConfig: template.Spec.Template.Spec.ControlPlaneConfig,
		},
	}

	if err := tcp.Default(context.Background(), tcp); err != nil {
		t.Fatalf("default failed: %v", err)
	}
	if _, err := tcp.ValidateCreate(context.Background(), tcp); err != nil {
		t.Fatalf("expected generated TalosControlPlane to validate after machineInfrastructure injection: %v", err)
	}
	if got := tcp.Spec.MachineTemplate.ObjectMeta.Labels["example.siderolabs.dev/control-plane"]; got != "true" {
		t.Fatalf("expected machineTemplate metadata to carry over, got %q", got)
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
