package netutil

import (
	"net"
	"testing"
)

func mustTCPAddr(t *testing.T, hostport string) *net.TCPAddr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", hostport)
	if err != nil {
		t.Fatalf("resolve %q: %v", hostport, err)
	}
	return addr
}

func TestNewAccessController_Errors(t *testing.T) {
	if _, err := NewAccessController("nope", nil); err == nil {
		t.Error("expected error for invalid mode")
	}
	if _, err := NewAccessController(AccessModeWhitelist, nil); err == nil {
		t.Error("expected error for empty whitelist")
	}
	if _, err := NewAccessController(AccessModeWhitelist, []string{"not-an-ip"}); err == nil {
		t.Error("expected error for invalid whitelist entry")
	}
	if _, err := NewAccessController(AccessModeWhitelist, []string{"203.0.113.0/24"}); err != nil {
		t.Errorf("unexpected error for valid whitelist: %v", err)
	}
	if _, err := NewAccessController(AccessModeIntranet, nil); err != nil {
		t.Errorf("unexpected error for intranet mode: %v", err)
	}
}

func TestAccessController_Intranet(t *testing.T) {
	ac, err := NewAccessController(AccessModeIntranet, nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	cases := map[string]bool{
		"10.0.0.5:1234":             true,
		"172.16.3.4:1":              true,
		"192.168.1.1:80":            true,
		"127.0.0.1:9":               true,
		"[::1]:9":                   true,
		"169.254.1.1:9":             true,  // link-local
		"8.8.8.8:53":                false, // public
		"[2001:4860:4860::8888]:53": false,
	}
	for hp, want := range cases {
		if got := ac.Allow(mustTCPAddr(t, hp)); got != want {
			t.Errorf("Allow(%s) = %v, want %v", hp, got, want)
		}
	}
}

func TestAccessController_Whitelist(t *testing.T) {
	ac, err := NewAccessController(AccessModeWhitelist, []string{"203.0.113.0/24", "8.8.8.8", "fd00::/8"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	cases := map[string]bool{
		"203.0.113.7:1": true,
		"8.8.8.8:1":     true,
		"8.8.4.4:1":     false,
		"10.0.0.1:1":    false,
		"[fd00::1]:1":   true,
		"[fe80::1]:1":   false,
	}
	for hp, want := range cases {
		if got := ac.Allow(mustTCPAddr(t, hp)); got != want {
			t.Errorf("Allow(%s) = %v, want %v", hp, got, want)
		}
	}
}

func TestAccessController_FailClosed(t *testing.T) {
	ac, _ := NewAccessController(AccessModeIntranet, nil)
	if ac.Allow(nil) {
		t.Error("nil addr must be denied")
	}
	var nilAC *AccessController
	if nilAC.Allow(mustTCPAddr(t, "127.0.0.1:1")) {
		t.Error("nil controller must deny")
	}
}
