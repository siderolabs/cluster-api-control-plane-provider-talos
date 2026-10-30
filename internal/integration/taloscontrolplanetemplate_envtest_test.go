// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	cabptv1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

func TestTalosControlPlaneTemplateWebhookIntegration(t *testing.T) {
	t.Parallel()

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "config", "webhook")},
		},
	}

	cfg, err := testEnv.Start()
	if err != nil {
		if strings.Contains(err.Error(), "fork/exec") || strings.Contains(err.Error(), "no such file or directory") {
			t.Skipf("envtest binaries are not available: %v", err)
		}

		t.Fatalf("failed to start envtest: %v", err)
	}

	t.Cleanup(func() {
		if stopErr := testEnv.Stop(); stopErr != nil {
			t.Fatalf("failed to stop envtest: %v", stopErr)
		}
	})

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(controlplanev1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    testEnv.WebhookInstallOptions.LocalServingHost,
			Port:    testEnv.WebhookInstallOptions.LocalServingPort,
			CertDir: testEnv.WebhookInstallOptions.LocalServingCertDir,
		}),
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	if err := (&controlplanev1.TalosControlPlane{}).SetupWebhookWithManager(mgr); err != nil {
		t.Fatalf("failed to setup taloscontrolplane webhook: %v", err)
	}
	if err := (&controlplanev1.TalosControlPlaneTemplate{}).SetupWebhookWithManager(mgr); err != nil {
		t.Fatalf("failed to setup taloscontrolplanetemplate webhook: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() {
		if runErr := mgr.Start(ctx); runErr != nil {
			errCh <- fmt.Errorf("manager exited with error: %w", runErr)
		}
	}()

	t.Cleanup(func() {
		select {
		case runErr := <-errCh:
			if runErr != nil && !strings.Contains(runErr.Error(), "context canceled") {
				t.Fatalf("manager run error: %v", runErr)
			}
		default:
		}
	})

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "defaulting-test"}}
	if err := k8sClient.Create(ctx, namespace); err != nil {
		t.Fatalf("failed to create namespace: %v", err)
	}

	direct := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "direct-machine-template",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.31.0",
			MachineTemplate: controlplanev1.TalosControlPlaneMachineTemplate{
				Spec: controlplanev1.TalosControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: "infrastructure.cluster.x-k8s.io",
						Kind:     "DockerMachineTemplate",
						Name:     "cp-template",
					},
				},
			},
			ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
				ControlPlaneConfig: talosConfigSpec("controlplane", nil),
			},
		},
	}
	if err := k8sClient.Create(ctx, direct); err != nil {
		t.Fatalf("expected TalosControlPlane create with machineTemplate.spec.infrastructureRef to succeed: %v", err)
	}

	persistedDirect := &controlplanev1.TalosControlPlane{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(direct), persistedDirect); err != nil {
		t.Fatalf("failed to get persisted TalosControlPlane: %v", err)
	}
	if got := persistedDirect.Spec.MachineTemplate.Spec.InfrastructureRef.Name; got != "cp-template" {
		t.Fatalf("expected machineTemplate.spec.infrastructureRef name %q, got %q", "cp-template", got)
	}

	missingRef := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "direct-missing-ref",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneSpec{
			Version: "v1.31.0",
			ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
				ControlPlaneConfig: talosConfigSpec("controlplane", nil),
			},
		},
	}
	if err := k8sClient.Create(ctx, missingRef); err == nil {
		t.Fatalf("expected TalosControlPlane without machineTemplate.spec.infrastructureRef to be rejected")
	}

	topologyTemplate := &controlplanev1.TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "clusterclass-template",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneTemplateSpec{
			Template: controlplanev1.TalosControlPlaneTemplateResource{
				Spec: controlplanev1.TalosControlPlaneTemplateResourceSpec{
					ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
						ControlPlaneConfig: talosConfigSpec("controlplane", []string{"machine:\n  install:\n    disk: /dev/sda\n"}),
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, topologyTemplate); err != nil {
		t.Fatalf("expected TalosControlPlaneTemplate without infrastructureRef to succeed for ClusterClass machineInfrastructure flow: %v", err)
	}

	valid := &controlplanev1.TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "valid-template",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneTemplateSpec{
			Template: controlplanev1.TalosControlPlaneTemplateResource{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{
						"cluster.x-k8s.io/cluster-class-name": "talos-cp",
					},
				},
				Spec: controlplanev1.TalosControlPlaneTemplateResourceSpec{
					MachineTemplate: controlplanev1.TalosControlPlaneTemplateMachineTemplate{
						ObjectMeta: clusterv1.ObjectMeta{
							Labels: map[string]string{
								"cluster.x-k8s.io/cluster-class-name": "talos-cp",
							},
						},
						Spec: controlplanev1.TalosControlPlaneTemplateMachineTemplateSpec{
							Deletion: controlplanev1.TalosControlPlaneMachineTemplateDeletionSpec{
								NodeDeletionTimeoutSeconds: func() *int32 { v := int32(60); return &v }(),
							},
						},
					},
					ControlPlaneConfig: controlplanev1.ControlPlaneConfig{
						ControlPlaneConfig: talosConfigSpec("controlplane", nil),
					},
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, valid); err != nil {
		t.Fatalf("expected valid template create to succeed: %v", err)
	}

	persisted := &controlplanev1.TalosControlPlaneTemplate{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(valid), persisted); err != nil {
		t.Fatalf("failed to get persisted template: %v", err)
	}

	if persisted.Spec.Template.Spec.RolloutStrategy == nil {
		t.Fatalf("expected rollout strategy defaults to be persisted")
	}
	if persisted.Spec.Template.Spec.RolloutStrategy.RollingUpdate == nil || persisted.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge == nil {
		t.Fatalf("expected rollingUpdate.maxSurge default to be set")
	}
	if got := persisted.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue(); got != 1 {
		t.Fatalf("expected default maxSurge=1, got %d", got)
	}

	invalid := &controlplanev1.TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "invalid-template",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneTemplateSpec{
			Template: controlplanev1.TalosControlPlaneTemplateResource{
				Spec: controlplanev1.TalosControlPlaneTemplateResourceSpec{
					RolloutStrategy: &controlplanev1.RolloutStrategy{Type: controlplanev1.RolloutStrategyType("Invalid")},
				},
			},
		},
	}

	err = k8sClient.Create(ctx, invalid)
	if err == nil {
		t.Fatalf("expected invalid rolloutStrategy type to be rejected")
	}
	if !strings.Contains(err.Error(), "valid values are") {
		t.Fatalf("expected validation error to mention valid values, got: %v", err)
	}

	persisted.Spec.Template.Spec.RolloutStrategy = &controlplanev1.RolloutStrategy{
		Type: controlplanev1.RollingUpdateStrategyType,
		RollingUpdate: &controlplanev1.RollingUpdate{MaxSurge: func() *intstr.IntOrString {
			v := intstr.FromInt(2)
			return &v
		}()},
	}
	err = k8sClient.Update(ctx, persisted)
	if err == nil {
		t.Fatalf("expected template spec update to be rejected")
	}
	if !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected immutability error, got: %v", err)
	}

	metadataOnly := &controlplanev1.TalosControlPlaneTemplate{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(valid), metadataOnly); err != nil {
		t.Fatalf("failed to reload template: %v", err)
	}
	metadataOnly.Labels = map[string]string{
		"example.siderolabs.dev/revision": "2",
	}
	if err := k8sClient.Update(ctx, metadataOnly); err != nil {
		t.Fatalf("expected metadata-only update to succeed: %v", err)
	}
}

func talosConfigSpec(generateType string, strategicPatches []string) cabptv1.TalosConfigSpec {
	return cabptv1.TalosConfigSpec{
		GenerateType:     generateType,
		StrategicPatches: strategicPatches,
	}
}
