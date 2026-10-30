// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build !race

package v1alpha3

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"
	"sigs.k8s.io/randfill"

	cpv1beta1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// The test is disabled with the race detector (via "//go:build !race" above), as the fuzz tests would time out,
// like the Cluster API ones.

// fuzzTestGVKs are the infrastructure templates the references are fuzzed with, and the versions the contract maps
// their kinds to.
var fuzzTestGVKs = []schema.GroupVersionKind{
	{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta2", Kind: "DockerMachineTemplate"},
	{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Kind: "MetalMachineTemplate"},
	{Group: "infrastructure.example.com", Version: "v1alpha1", Kind: "ExampleMachineTemplate"},
}

func TestFuzzyConversion(t *testing.T) {
	previous := apiVersionGetter
	t.Cleanup(func() { apiVersionGetter = previous })

	SetAPIVersionGetter(func(gk schema.GroupKind) (string, error) {
		for _, gvk := range fuzzTestGVKs {
			if gvk.GroupKind() == gk {
				return gvk.GroupVersion().String(), nil
			}
		}

		return "", fmt.Errorf("failed to map GroupKind %s to version", gk)
	})

	scheme := runtime.NewScheme()
	utilruntime.Must(AddToScheme(scheme))
	utilruntime.Must(cpv1beta1.AddToScheme(scheme))

	t.Run("for TalosControlPlane", utilconversion.FuzzTestFunc(utilconversion.FuzzTestFuncInput{
		Scheme:      scheme,
		Hub:         &cpv1beta1.TalosControlPlane{},
		Spoke:       &TalosControlPlane{},
		FuzzerFuncs: []fuzzer.FuzzerFuncs{talosControlPlaneFuzzFuncs},
	}))
}

func talosControlPlaneFuzzFuncs(_ runtimeserializer.CodecFactory) []any {
	return []any{
		hubMachineTemplateSpec,
		hubTalosControlPlaneStatus,
		spokeTalosControlPlane,
		spokeTalosControlPlaneStatus,
		jsonValue,
	}
}

// jsonValue fills the values of the config patches with valid JSON, as the API server only stores valid JSON.
func jsonValue(in *apiextensionsv1.JSON, c randfill.Continue) {
	var value map[string]string

	c.Fill(&value)

	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}

	in.Raw = raw
}

func hubTalosControlPlaneStatus(in *cpv1beta1.TalosControlPlaneStatus, c randfill.Continue) {
	c.FillNoCustom(in)

	// The controller sets the deprecated initialized together with controlPlaneInitialized, and the v1alpha3
	// initialized is derived from controlPlaneInitialized.
	initialized := ptr.Deref(in.Initialization.ControlPlaneInitialized, false)
	if initialized && in.Deprecated == nil {
		in.Deprecated = &cpv1beta1.TalosControlPlaneDeprecatedStatus{}
	}
	if initialized && in.Deprecated.V1Beta1 == nil {
		in.Deprecated.V1Beta1 = &cpv1beta1.TalosControlPlaneV1Beta1DeprecatedStatus{}
	}
	if in.Deprecated != nil && in.Deprecated.V1Beta1 != nil {
		in.Deprecated.V1Beta1.Initialized = initialized
	}

	// A deprecated field without any value is not kept, the same as a missing one.
	if in.Deprecated != nil && in.Deprecated.V1Beta1 != nil &&
		reflect.DeepEqual(*in.Deprecated.V1Beta1, cpv1beta1.TalosControlPlaneV1Beta1DeprecatedStatus{}) {
		in.Deprecated.V1Beta1 = nil
	}
	if in.Deprecated != nil && in.Deprecated.V1Beta1 == nil {
		in.Deprecated = nil
	}
}

func spokeTalosControlPlaneStatus(in *TalosControlPlaneStatus, c randfill.Continue) {
	c.FillNoCustom(in)

	// The controller sets the initialization together with initialized.
	if in.Initialized || ptr.Deref(in.Initialization.ControlPlaneInitialized, false) {
		in.Initialized = true
		in.Initialization.ControlPlaneInitialized = ptr.To(true)
	}

	// The fields v0.6.0-alpha.0 added come with the v1beta2 field: without it, the status was written by that
	// release, which is converted one way only. Otherwise, a v1beta2 field without conditions is not kept, the same
	// as a missing one.
	hasV060Alpha0Fields := in.AvailableReplicas != nil || in.UpToDateReplicas != nil || in.Initialization.ControlPlaneInitialized != nil

	switch {
	case hasV060Alpha0Fields && in.V1Beta2 == nil:
		in.V1Beta2 = &TalosControlPlaneV1Beta2Status{}
	case !hasV060Alpha0Fields && in.V1Beta2 != nil && in.V1Beta2.Conditions == nil:
		in.V1Beta2 = nil
	}
}

func hubMachineTemplateSpec(in *cpv1beta1.TalosControlPlaneMachineTemplateSpec, c randfill.Continue) {
	c.FillNoCustom(in)

	// The reference always points to a known template kind. It is required, so it always has a name.
	gvk := fuzzTestGVKs[c.Intn(len(fuzzTestGVKs))]
	in.InfrastructureRef.APIGroup = gvk.Group
	in.InfrastructureRef.Kind = gvk.Kind
	if in.InfrastructureRef.Name == "" {
		in.InfrastructureRef.Name = "infra"
	}
}

func spokeTalosControlPlane(in *TalosControlPlane, c randfill.Continue) {
	c.FillNoCustom(in)

	// The reference always points to a known template kind, in the version the contract maps it to, in the namespace
	// of the TalosControlPlane: the hub reference has no other fields. It is required, so it always has a name.
	gvk := fuzzTestGVKs[c.Intn(len(fuzzTestGVKs))]
	if in.Spec.InfrastructureTemplate.Name == "" {
		in.Spec.InfrastructureTemplate.Name = "infra"
	}
	in.Spec.InfrastructureTemplate.APIVersion = gvk.GroupVersion().String()
	in.Spec.InfrastructureTemplate.Kind = gvk.Kind
	in.Spec.InfrastructureTemplate.Namespace = in.Namespace
	in.Spec.InfrastructureTemplate.UID = ""
	in.Spec.InfrastructureTemplate.ResourceVersion = ""
	in.Spec.InfrastructureTemplate.FieldPath = ""
}
