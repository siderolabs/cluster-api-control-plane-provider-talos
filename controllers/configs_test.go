// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	controlplanev1 "github.com/siderolabs/cluster-api-control-plane-provider-talos/api/v1beta1"
)

func TestTalosEndpoint(t *testing.T) {
	for _, tt := range []struct {
		name    string
		address string
		want    string
		wantOK  bool
	}{
		{name: "IPv4", address: "192.168.122.29", want: "192.168.122.29", wantOK: true},
		{name: "IPv6", address: "2001:db8::1", want: "2001:db8::1", wantOK: true},
		{name: "host name", address: "cp-0.example.com", want: "cp-0.example.com", wantOK: true},
		{name: "IPv6 link-local with zone", address: "fe80::5054:ff:fe07:37d1%enp1s0"},
		{name: "IPv6 link-local without zone", address: "fe80::5054:ff:fe07:37d1"},
		{name: "IPv4 link-local", address: "169.254.10.1"},
		{name: "zone on a global address is dropped", address: "2001:db8::1%eth0", want: "2001:db8::1", wantOK: true},
		{name: "empty", address: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := talosEndpoint(tt.address)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("talosEndpoint(%q) = %q, %v, want %q, %v", tt.address, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestTalosEndpoints(t *testing.T) {
	endpoints, skipped := talosEndpoints([]string{"10.0.0.5", "", "fe80::1%eth0", "2001:db8::1", "169.254.1.1"})

	if want := []string{"10.0.0.5", "2001:db8::1"}; strings.Join(endpoints, ",") != strings.Join(want, ",") {
		t.Errorf("endpoints = %q, want %q", endpoints, want)
	}

	if want := []string{"fe80::1%eth0", "169.254.1.1"}; strings.Join(skipped, ",") != strings.Join(want, ",") {
		t.Errorf("skipped = %q, want %q", skipped, want)
	}
}

func TestNoEndpointsError(t *testing.T) {
	err := noEndpointsError("machine", "cp-0", []string{"fe80::1%eth0"})
	if msg := err.Error(); !strings.Contains(msg, `"cp-0"`) || !strings.Contains(msg, "link-local") || !strings.Contains(msg, "fe80::1%eth0") {
		t.Errorf("error with skipped addresses = %q, want it to name the machine and the skipped link-local address", msg)
	}

	err = noEndpointsError("machine", "cp-0", nil)
	if msg := err.Error(); !strings.Contains(msg, `"cp-0"`) || strings.Contains(msg, "link-local") {
		t.Errorf("error without skipped addresses = %q, want it to name the machine and not mention link-local addresses", msg)
	}
}

// TestTalosconfigForMachinesRequiresEndpointPerMachine checks that every machine needs an address usable as a Talos API
// endpoint, not only the first one.
func TestTalosconfigForMachinesRequiresEndpointPerMachine(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clusterv1.AddToScheme, controlplanev1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	tcp := &controlplanev1.TalosControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp",
			Namespace: "default",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "foo"},
		},
	}

	talosconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "foo-talosconfig", Namespace: "default"},
		Data: map[string][]byte{
			"talosconfig": []byte("context: foo\ncontexts:\n  foo:\n    endpoints:\n      - 10.0.0.1\n"),
		},
	}

	r := &TalosControlPlaneReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(talosconfigSecret).Build(),
		Log:    testr.New(t),
		Scheme: scheme,
	}

	machine := func(name string, addresses ...clusterv1.MachineAddress) clusterv1.Machine {
		return clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Status:     clusterv1.MachineStatus{Addresses: addresses},
		}
	}

	for _, tt := range []struct {
		name     string
		machines []clusterv1.Machine
		wantErr  []string
	}{
		{
			name: "second machine has only a link-local address",
			machines: []clusterv1.Machine{
				machine("cp-0", clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: "10.0.0.5"}),
				machine("cp-1", clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: "fe80::5054:ff:fe07:37d1%enp1s0"}),
			},
			wantErr: []string{`"cp-1"`, "link-local", "fe80::5054:ff:fe07:37d1%enp1s0"},
		},
		{
			name: "second machine has no address yet",
			machines: []clusterv1.Machine{
				machine("cp-0", clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: "10.0.0.5"}),
				machine("cp-1", clusterv1.MachineAddress{Type: clusterv1.MachineHostName, Address: "cp-1"}),
			},
			wantErr: []string{`"cp-1"`, "doesn't have any InternalIP or ExternalIP address yet"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := r.talosconfigForMachines(context.Background(), tcp, tt.machines...)
			if err == nil {
				c.Close() //nolint:errcheck

				t.Fatal("expected an error")
			}

			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q doesn't contain %q", err, want)
				}
			}
		})
	}
}
