// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"testing"

	cabptv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	cabptv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilconversion "sigs.k8s.io/cluster-api/util/conversion"
	"sigs.k8s.io/randfill"

	cpv1beta1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

// The bootstrap provider's machine configuration spec is embedded in this provider's CRD: the
// v1alpha3 API embeds the bootstrap provider's v1alpha3 shape and the v1beta1 API embeds its
// v1beta1 shape, bridged by hand-written conversions.
//
// The two shapes are field-identical except for v1beta1's imageFactory, so a field added to one
// and forgotten in the conversion would be dropped silently rather than failing to compile. These
// tests fuzz every field instead of asserting on a fixed example so that such a field is caught
// when it appears rather than after it has already lost someone's configuration. imageFactory is
// the known exception: it cannot survive the spec-level round trip and is covered by the
// object-level test below, which exercises the conversion-data annotation that carries it.
const conversionFuzzIterations = 200

func TestTalosConfigSpecRoundTripsFromV1Alpha3(t *testing.T) {
	t.Parallel()

	filler := randfill.New().NilChance(0.2).NumElements(0, 3)

	for range conversionFuzzIterations {
		var start cabptv1alpha3.TalosConfigSpec

		filler.Fill(&start)

		var hub cabptv1beta1.TalosConfigSpec

		require.NoError(t, Convert_v1alpha3_TalosConfigSpec_To_v1beta1_TalosConfigSpec(&start, &hub, nil))

		var back cabptv1alpha3.TalosConfigSpec

		require.NoError(t, Convert_v1beta1_TalosConfigSpec_To_v1alpha3_TalosConfigSpec(&hub, &back, nil))

		require.Equal(t, start, back)
	}
}

// Starting from the hub is the direction that catches a v1beta1-only field: converting down to
// v1alpha3 and back would quietly lose it.
func TestTalosConfigSpecRoundTripsFromV1Beta1(t *testing.T) {
	t.Parallel()

	filler := randfill.New().NilChance(0.2).NumElements(0, 3)

	for range conversionFuzzIterations {
		var start cabptv1beta1.TalosConfigSpec

		filler.Fill(&start)

		// v1alpha3 has no imageFactory; TalosControlPlane.ConvertTo restores it, see below.
		start.ImageFactory = nil

		var spoke cabptv1alpha3.TalosConfigSpec

		require.NoError(t, Convert_v1beta1_TalosConfigSpec_To_v1alpha3_TalosConfigSpec(&start, &spoke, nil))

		var back cabptv1beta1.TalosConfigSpec

		require.NoError(t, Convert_v1alpha3_TalosConfigSpec_To_v1beta1_TalosConfigSpec(&spoke, &back, nil))

		require.Equal(t, start, back)
	}
}

// imageFactory exists only in the hub's embedded specs. A TalosControlPlane converted down to
// v1alpha3 and back must carry both blocks through the conversion-data annotation.
func TestTalosControlPlaneRoundTripPreservesImageFactory(t *testing.T) {
	t.Parallel()

	block := func(ext string) *cabptv1beta1.ImageFactorySpec {
		return &cabptv1beta1.ImageFactorySpec{
			Extensions:      []string{ext},
			ExtraKernelArgs: []string{"talos.logging.kernel=udp://10.0.0.5:514/"},
			Overlay:         &cabptv1beta1.ImageFactoryOverlay{Name: "rpi_generic", Image: "ghcr.io/siderolabs/sbc-raspberrypi"},
			Bootloader:      "sd-boot",
		}
	}

	hub := &cpv1beta1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "default"},
		Spec: cpv1beta1.TalosControlPlaneSpec{
			Version: "v1.34.0",
			ControlPlaneConfig: cpv1beta1.ControlPlaneConfig{
				InitConfig:         cabptv1beta1.TalosConfigSpec{GenerateType: "init", TalosVersion: "v1.14", ImageFactory: block("siderolabs/nvme-cli")},
				ControlPlaneConfig: cabptv1beta1.TalosConfigSpec{GenerateType: "controlplane", TalosVersion: "v1.14", ImageFactory: block("siderolabs/intel-ucode")},
			},
		},
	}

	spoke := &TalosControlPlane{}
	require.NoError(t, spoke.ConvertFrom(hub))
	require.Contains(t, spoke.Annotations, utilconversion.DataAnnotation, "hub data must be stashed for the way back")
	require.Nil(t, spoke.Spec.ControlPlaneConfig.ControlPlaneConfig.ConfigPatches)

	restored := &cpv1beta1.TalosControlPlane{}
	require.NoError(t, spoke.ConvertTo(restored))

	require.Equal(t, hub.Spec.ControlPlaneConfig.InitConfig.ImageFactory, restored.Spec.ControlPlaneConfig.InitConfig.ImageFactory)
	require.Equal(t, hub.Spec.ControlPlaneConfig.ControlPlaneConfig.ImageFactory, restored.Spec.ControlPlaneConfig.ControlPlaneConfig.ImageFactory)
}
