package outbound

import (
	"testing"

	"github.com/sagernet/sing-box/option"
)

// ---------------------------------------------------------------------------
// parseUpstreamProxyURL
// ---------------------------------------------------------------------------

func TestParseUpstreamProxy_Socks5(t *testing.T) {
	spec, err := parseUpstreamProxyURL("socks5://proxy.example.local:1080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.outboundType != "socks" {
		t.Fatalf("outboundType: got %q, want %q", spec.outboundType, "socks")
	}
	if !spec.serverIsDomain {
		t.Fatal("serverIsDomain: expected true for hostname")
	}
	opts, ok := spec.options.(*option.SOCKSOutboundOptions)
	if !ok {
		t.Fatalf("options type: got %T, want *option.SOCKSOutboundOptions", spec.options)
	}
	if opts.Server != "proxy.example.local" || opts.ServerPort != 1080 {
		t.Fatalf("server: got %s:%d, want proxy.example.local:1080", opts.Server, opts.ServerPort)
	}
	if opts.Version != "5" {
		t.Fatalf("socks version: got %q, want %q", opts.Version, "5")
	}
}

func TestParseUpstreamProxy_Socks4(t *testing.T) {
	spec, err := parseUpstreamProxyURL("socks://10.0.0.1:3128")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.serverIsDomain {
		t.Fatal("serverIsDomain: expected false for IP address")
	}
	opts, ok := spec.options.(*option.SOCKSOutboundOptions)
	if !ok {
		t.Fatalf("options type: got %T, want *option.SOCKSOutboundOptions", spec.options)
	}
	if opts.Server != "10.0.0.1" || opts.ServerPort != 3128 {
		t.Fatalf("server: got %s:%d", opts.Server, opts.ServerPort)
	}
}

func TestParseUpstreamProxy_HTTP(t *testing.T) {
	spec, err := parseUpstreamProxyURL("http://host.docker.internal:20171")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.outboundType != "http" {
		t.Fatalf("outboundType: got %q, want %q", spec.outboundType, "http")
	}
	if !spec.serverIsDomain {
		t.Fatal("serverIsDomain: expected true for hostname")
	}
	opts, ok := spec.options.(*option.HTTPOutboundOptions)
	if !ok {
		t.Fatalf("options type: got %T, want *option.HTTPOutboundOptions", spec.options)
	}
	if opts.Server != "host.docker.internal" || opts.ServerPort != 20171 {
		t.Fatalf("server: got %s:%d", opts.Server, opts.ServerPort)
	}
}

func TestParseUpstreamProxy_SchemeLess(t *testing.T) {
	spec, err := parseUpstreamProxyURL("192.168.1.1:8080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.outboundType != "http" {
		t.Fatalf("outboundType: got %q, want %q", spec.outboundType, "http")
	}
	if spec.serverIsDomain {
		t.Fatal("serverIsDomain: expected false for IP address")
	}
}

func TestParseUpstreamProxy_HTTPS(t *testing.T) {
	spec, err := parseUpstreamProxyURL("https://proxy.example.com:8443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.outboundType != "http" {
		t.Fatalf("outboundType: got %q, want %q", spec.outboundType, "http")
	}
	opts, ok := spec.options.(*option.HTTPOutboundOptions)
	if !ok {
		t.Fatalf("options type: got %T, want *option.HTTPOutboundOptions", spec.options)
	}
	if opts.TLS == nil {
		t.Fatal("expected TLS config for https scheme")
	}
	if opts.TLS != nil && opts.TLS.ServerName != "proxy.example.com" {
		t.Fatalf("TLS ServerName: got %q, want %q", opts.TLS.ServerName, "proxy.example.com")
	}
}

func TestParseUpstreamProxy_WithAuth(t *testing.T) {
	spec, err := parseUpstreamProxyURL("socks5://user:pass@proxy.example.local:1080")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts, ok := spec.options.(*option.SOCKSOutboundOptions)
	if !ok {
		t.Fatalf("options type: got %T", spec.options)
	}
	if opts.Username != "user" || opts.Password != "pass" {
		t.Fatalf("auth: got %s:%s, want user:pass", opts.Username, opts.Password)
	}
}

func TestParseUpstreamProxy_DefaultPorts(t *testing.T) {
	cases := []struct {
		raw      string
		wantPort uint16
	}{
		{"socks5://host", 1080},
		{"http://host", 80},
		{"https://host", 443},
	}
	for _, tc := range cases {
		spec, err := parseUpstreamProxyURL(tc.raw)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.raw, err)
		}
		switch opts := spec.options.(type) {
		case *option.SOCKSOutboundOptions:
			if opts.ServerPort != tc.wantPort {
				t.Fatalf("%s: port got %d, want %d", tc.raw, opts.ServerPort, tc.wantPort)
			}
		case *option.HTTPOutboundOptions:
			if opts.ServerPort != tc.wantPort {
				t.Fatalf("%s: port got %d, want %d", tc.raw, opts.ServerPort, tc.wantPort)
			}
		}
	}
}

func TestParseUpstreamProxy_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"://host",
		"ftp://proxy",
		"socks5://:0",
	}
	for _, raw := range invalid {
		_, err := parseUpstreamProxyURL(raw)
		if err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

// ---------------------------------------------------------------------------
// injectUpstreamProxyDetour
// ---------------------------------------------------------------------------

func TestInjectUpstreamProxyDetour_SOCKS(t *testing.T) {
	opts := &option.SOCKSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: "node.example.com", ServerPort: 443},
	}
	injectUpstreamProxyDetour(opts)
	if opts.Detour != upstreamProxyOutboundTag {
		t.Fatalf("Detour: got %q, want %q", opts.Detour, upstreamProxyOutboundTag)
	}
}

func TestInjectUpstreamProxyDetour_PreservesExisting(t *testing.T) {
	opts := &option.SOCKSOutboundOptions{
		DialerOptions: option.DialerOptions{
			Detour: "custom-detour",
		},
	}
	injectUpstreamProxyDetour(opts)
	if opts.Detour != "custom-detour" {
		t.Fatalf("Detour overwritten: got %q, want %q", opts.Detour, "custom-detour")
	}
}

func TestInjectUpstreamProxyDetour_UnsupportedType(t *testing.T) {
	// A struct that does not embed DialerOptions should be skipped.
	injectUpstreamProxyDetour("not-a-pointer-struct")
	// No panic — just no-op.
}
