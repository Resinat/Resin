package proxy

import (
	"testing"

	"github.com/resin-proxy/resin/internal/model"
)

func TestNormalizeRulePrefix(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{
			name:  "TrimAndLowercaseHost",
			input: "  API.Example.COM/v1  ",
			want:  "api.example.com/v1",
		},
		{
			name:  "Wildcard",
			input: "*",
			want:  "*",
		},
		{
			name:    "Empty",
			input:   "",
			wantErr: "url_prefix: must be non-empty",
		},
		{
			name:    "HostEmpty",
			input:   "/v1",
			wantErr: "url_prefix: host must be non-empty",
		},
		{
			name:    "ContainsQuery",
			input:   "api.example.com/v1?x=1",
			wantErr: "url_prefix: must not contain '?'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRulePrefix(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("error = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalized prefix = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildAccountMatcher_UsesSharedRulePrefixNormalization(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		// Leading/trailing spaces should be normalized by shared helper.
		{URLPrefix: "  API.Example.COM/v1  ", Headers: []string{"x-good"}, UpdatedAtNs: 10},
		// Invalid prefix with query should be ignored by matcher build path.
		{URLPrefix: "api.example.com/v1?x=1", Headers: []string{"x-bad"}, UpdatedAtNs: 20},
		{URLPrefix: "*", Headers: []string{"x-fallback"}, UpdatedAtNs: 1},
	})

	prefix, headers := m.MatchWithPrefix("api.example.com", "/v1/orders")
	if prefix != "api.example.com/v1" {
		t.Fatalf("prefix = %q, want %q", prefix, "api.example.com/v1")
	}
	if len(headers) != 1 || headers[0] != "x-good" {
		t.Fatalf("headers = %v, want %v", headers, []string{"x-good"})
	}
}
