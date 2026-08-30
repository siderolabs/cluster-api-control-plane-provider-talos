// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package runtimeclient

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	runtimecatalog "sigs.k8s.io/cluster-api/exp/runtime/catalog"
)

func gvhFor(t *testing.T, hook runtimecatalog.Hook) runtimecatalog.GroupVersionHook {
	t.Helper()

	catalog := runtimecatalog.New()
	require.NoError(t, runtimehooksv1.AddToCatalog(catalog))

	gvh, err := catalog.GroupVersionHook(hook)
	require.NoError(t, err)

	return gvh
}

// The expected URLs are spelled out rather than derived from GVHToPath, because deriving them
// would pass against any path the implementation happens to build. A runtime extension server
// registers handlers under /<group>/<version>/<hook>/<name>; posting to the bare handler name
// reaches the server and returns 404, which is how this shipped broken.
func TestEndpointForUsesTheHookPathTheServerRegisters(t *testing.T) {
	t.Parallel()

	handler := &runtimev1.ExtensionHandler{Name: "can-update-machine.cabpt-talos-in-place-updates"}
	gvh := gvhFor(t, runtimehooksv1.CanUpdateMachine)

	for name, tc := range map[string]struct {
		clientConfig runtimev1.ClientConfig
		expected     string
	}{
		"service": {
			clientConfig: runtimev1.ClientConfig{
				Service: runtimev1.ServiceReference{
					Name:      "cabpt-runtime-extension-service",
					Namespace: "talos-bootstrap-system",
					Port:      ptr(int32(443)),
				},
			},
			expected: "https://cabpt-runtime-extension-service.talos-bootstrap-system.svc:443" +
				"/hooks.runtime.cluster.x-k8s.io/v1alpha1/canupdatemachine/can-update-machine.cabpt-talos-in-place-updates",
		},
		"url": {
			clientConfig: runtimev1.ClientConfig{URL: "https://extension.example.com"},
			expected: "https://extension.example.com" +
				"/hooks.runtime.cluster.x-k8s.io/v1alpha1/canupdatemachine/can-update-machine.cabpt-talos-in-place-updates",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := &runtimev1.ExtensionConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cabpt-talos-in-place-updates"},
				Spec:       runtimev1.ExtensionConfigSpec{ClientConfig: tc.clientConfig},
			}

			got, err := endpointFor(config, handler, gvh)
			require.NoError(t, err)
			require.Equal(t, tc.expected, got)
		})
	}
}

// UpdateMachine is the hook that actually performs the update, so pin its path too.
func TestEndpointForUpdateMachineHook(t *testing.T) {
	t.Parallel()

	config := &runtimev1.ExtensionConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cabpt-talos-in-place-updates"},
		Spec: runtimev1.ExtensionConfigSpec{
			ClientConfig: runtimev1.ClientConfig{
				Service: runtimev1.ServiceReference{
					Name:      "svc",
					Namespace: "ns",
					Port:      ptr(int32(443)),
				},
			},
		},
	}

	got, err := endpointFor(config, &runtimev1.ExtensionHandler{Name: "update-machine.ext"}, gvhFor(t, runtimehooksv1.UpdateMachine))
	require.NoError(t, err)
	require.Equal(t, "https://svc.ns.svc:443/hooks.runtime.cluster.x-k8s.io/v1alpha1/updatemachine/update-machine.ext", got)
}

func ptr[T any](v T) *T {
	return &v
}
