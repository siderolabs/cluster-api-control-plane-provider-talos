// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package v1alpha3

import (
	"testing"

	cabptv1alpha3 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1alpha3"
	cabptv1beta1 "github.com/siderolabs/cluster-api-bootstrap-provider-talos/api/v1beta1"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/randfill"
)

// The bootstrap provider's machine configuration spec is embedded in this provider's CRD: the
// v1alpha3 API embeds the bootstrap provider's v1alpha3 shape and the v1beta1 API embeds its
// v1beta1 shape, bridged by hand-written conversions.
//
// The two shapes are field-identical today, so a field added to one and forgotten in the
// conversion would be dropped silently rather than failing to compile. These tests fuzz every
// field instead of asserting on a fixed example so that such a field is caught when it appears
// rather than after it has already lost someone's configuration.
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

		var spoke cabptv1alpha3.TalosConfigSpec

		require.NoError(t, Convert_v1beta1_TalosConfigSpec_To_v1alpha3_TalosConfigSpec(&start, &spoke, nil))

		var back cabptv1beta1.TalosConfigSpec

		require.NoError(t, Convert_v1alpha3_TalosConfigSpec_To_v1beta1_TalosConfigSpec(&spoke, &back, nil))

		require.Equal(t, start, back)
	}
}
