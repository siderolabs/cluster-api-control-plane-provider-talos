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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1alpha3"
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

	valid := &controlplanev1.TalosControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "valid-template",
			Namespace: namespace.Name,
		},
		Spec: controlplanev1.TalosControlPlaneTemplateSpec{
			Template: controlplanev1.TalosControlPlaneTemplateResource{
				Spec: controlplanev1.TalosControlPlaneTemplateResourceSpec{
					InfrastructureTemplate: corev1.ObjectReference{
						APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
						Kind:       "DockerMachineTemplate",
						Name:       "cp-template",
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

	if got := persisted.Spec.Template.Spec.InfrastructureTemplate.Namespace; got != namespace.Name {
		t.Fatalf("expected infrastructure template namespace to default to %q, got %q", namespace.Name, got)
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
					InfrastructureTemplate: corev1.ObjectReference{
						APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
						Kind:       "DockerMachineTemplate",
						Name:       "cp-template",
					},
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

	persisted.Spec.Template.Spec.RolloutStrategy = &controlplanev1.RolloutStrategy{Type: controlplanev1.RolloutStrategyType("Invalid")}
	err = k8sClient.Update(ctx, persisted)
	if err == nil {
		t.Fatalf("expected invalid rolloutStrategy update to be rejected")
	}

	persisted.Spec.Template.Spec.RolloutStrategy = &controlplanev1.RolloutStrategy{
		Type: controlplanev1.RollingUpdateStrategyType,
		RollingUpdate: &controlplanev1.RollingUpdate{MaxSurge: func() *intstr.IntOrString {
			v := intstr.FromInt(2)
			return &v
		}()},
	}
	if err := k8sClient.Update(ctx, persisted); err != nil {
		t.Fatalf("expected valid update to succeed: %v", err)
	}

	eventuallyUpdated := &controlplanev1.TalosControlPlaneTemplate{}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(valid), eventuallyUpdated); err != nil {
			t.Fatalf("failed to get updated template: %v", err)
		}
		if eventuallyUpdated.Spec.Template.Spec.RolloutStrategy != nil &&
			eventuallyUpdated.Spec.Template.Spec.RolloutStrategy.RollingUpdate != nil &&
			eventuallyUpdated.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge != nil &&
			eventuallyUpdated.Spec.Template.Spec.RolloutStrategy.RollingUpdate.MaxSurge.IntValue() == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for updated rollout strategy to persist")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
