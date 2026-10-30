// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
