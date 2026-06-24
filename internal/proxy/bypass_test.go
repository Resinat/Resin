package proxy

import "testing"

func TestTargetBypassMatcher_MatchesCommonNoProxyRules(t *testing.T) {
	matcher := NewTargetBypassMatcher([]string{
		"localhost",
		"127.*",
		"192.168.*",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"::1",
		"<local>",
		"*.corp.local",
	})

	tests := []struct {
		target string
		want   bool
	}{
		{"localhost:3000", true},
		{"127.0.0.1:8080", true},
		{"192.168.1.1", true},
		{"10.23.45.67:443", true},
		{"172.16.0.1:80", true},
		{"172.31.255.254", true},
		{"172.32.0.1", false},
		{"[::1]:8443", true},
		{"printer:9100", true},
		{"api.corp.local:443", true},
		{"api.example.com:443", false},
	}

	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			if got := matcher.ShouldBypass(tc.target); got != tc.want {
				t.Fatalf("ShouldBypass(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

func TestTargetBypassMatcher_EmptyRulesDoesNotMatch(t *testing.T) {
	if matcher := NewTargetBypassMatcher(nil); matcher != nil {
		t.Fatal("empty rules should return nil matcher")
	}
}
