package config

import "testing"

func TestIsWeakToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		weak  bool
	}{
		{name: "empty_token", token: "", weak: false},
		{name: "common_password", token: "password", weak: true},
		{name: "all_same", token: "aaaaaaaaaaaa", weak: true},
		{name: "simple_sequence", token: "1234567890", weak: true},
		{name: "short_mixed", token: "Ab1!", weak: true},
		{name: "long_hex", token: "a9f73d18e5249b6a35f7419d11c603e2", weak: false},
		{name: "mixed_strong", token: "Resin-2026-Admin!Token", weak: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsWeakToken(tt.token)
			if got != tt.weak {
				t.Fatalf("IsWeakToken(%q) = %v, want %v", tt.token, got, tt.weak)
			}
		})
	}
}

func TestIsWeakToken_ZXCVBNThreshold(t *testing.T) {
	// Threshold policy: score < 3 is weak.
	if !IsWeakToken("ResinAdmin2026") {
		t.Fatal("expected score-2 token to be weak")
	}
	if IsWeakToken("ZTbmfJR") {
		t.Fatal("expected score-3 token to be non-weak")
	}
}
