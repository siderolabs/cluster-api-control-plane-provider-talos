// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package controllers

import "testing"

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
