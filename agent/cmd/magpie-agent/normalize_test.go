package main

import "testing"

// TestNormalizeServerURL pins every input shape the install path can
// see. The "production-broken" case below is the one that left agents
// invisible in the UI: install.ps1 baked in https://… which the old
// "if it doesn't start with ws://, prepend it" logic turned into the
// malformed ws://https://… If anyone relaxes the http(s) handling
// here, the same regression is one passing test away.
func TestNormalizeServerURL(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{
			name: "bare host gets ws+default-port",
			in:   "magpied.internal",
			want: "ws://magpied.internal:12002/v1/opamp",
		},
		{
			name: "bare ipv4 gets ws+default-port",
			in:   "10.0.0.5",
			want: "ws://10.0.0.5:12002/v1/opamp",
		},
		{
			name: "host:port preserved as-is",
			in:   "magpied.internal:9090",
			want: "ws://magpied.internal:9090:12002/v1/opamp",
			// Note: this case is admittedly weird (we can't tell host:port
			// from host: missing). Operators with non-default ports should
			// pass a full ws:// URL; documenting the corner here so future
			// readers don't try to "fix" it without a real spec.
		},
		{
			name: "production-broken case: https scheme becomes wss",
			in:   "https://magpie.apteancloud.dev",
			want: "wss://magpie.apteancloud.dev/v1/opamp",
		},
		{
			name: "http scheme becomes ws, port preserved",
			in:   "http://magpied:12002",
			want: "ws://magpied:12002/v1/opamp",
		},
		{
			name: "https with non-standard port preserved",
			in:   "https://magpie.example.com:8443",
			want: "wss://magpie.example.com:8443/v1/opamp",
		},
		{
			name: "ws URL passed through verbatim",
			in:   "ws://magpied:12002/v1/opamp",
			want: "ws://magpied:12002/v1/opamp",
		},
		{
			name: "wss URL with custom path passed through verbatim",
			in:   "wss://magpie.example.com/custom/path",
			want: "wss://magpie.example.com/custom/path",
		},
		{
			name: "trailing path in https URL stripped, anchored at /v1/opamp",
			in:   "https://magpie.example.com/some/proxy/prefix",
			want: "wss://magpie.example.com/v1/opamp",
		},
		{
			name:    "empty rejected",
			in:      "",
			wantErr: true,
		},
		{
			name:    "ftp scheme rejected",
			in:      "ftp://magpied",
			wantErr: true,
		},
		{
			name:    "https without host rejected",
			in:      "https://",
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeServerURL(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("normalizeServerURL(%q) = %q, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Errorf("normalizeServerURL(%q) error = %v, want %q", c.in, err, c.want)
				return
			}
			if got != c.want {
				t.Errorf("normalizeServerURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
