package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/resin-proxy/resin/internal/model"
)

func TestAccountMatcher_ExactMatch(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["Authorization"]`},
		{URLPrefix: "api.example.com/v2", HeadersJSON: `["x-api-key"]`},
	})

	h := m.Match("api.example.com", "/v1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}

	h = m.Match("api.example.com", "/v2/data")
	if len(h) != 1 || h[0] != "x-api-key" {
		t.Fatalf("expected [x-api-key], got %v", h)
	}
}

func TestAccountMatcher_LongestPrefix(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com", HeadersJSON: `["Authorization"]`},
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["x-api-key"]`},
		{URLPrefix: "api.example.com/v1/admin", HeadersJSON: `["x-admin-key"]`},
	})

	// /v1/admin/users → longest match is api.example.com/v1/admin
	h := m.Match("api.example.com", "/v1/admin/users")
	if len(h) != 1 || h[0] != "x-admin-key" {
		t.Fatalf("expected [x-admin-key], got %v", h)
	}

	// /v1/other → longest match is api.example.com/v1
	h = m.Match("api.example.com", "/v1/other")
	if len(h) != 1 || h[0] != "x-api-key" {
		t.Fatalf("expected [x-api-key], got %v", h)
	}

	// /other → longest match is api.example.com
	h = m.Match("api.example.com", "/other")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcher_WildcardFallback(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["x-api-key"]`},
		{URLPrefix: "*", HeadersJSON: `["Authorization", "x-api-key"]`},
	})

	// Known prefix.
	h := m.Match("api.example.com", "/v1/users")
	if len(h) != 1 || h[0] != "x-api-key" {
		t.Fatalf("expected [x-api-key], got %v", h)
	}

	// Unknown domain → wildcard.
	h = m.Match("unknown.com", "/anything")
	if len(h) != 2 || h[0] != "Authorization" || h[1] != "x-api-key" {
		t.Fatalf("expected [Authorization, x-api-key], got %v", h)
	}
}

func TestAccountMatcher_CaseInsensitive(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "Api.Example.COM/V1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("API.EXAMPLE.COM", "/V1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcher_HostWithPort(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("api.example.com:443", "/v1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcher_IPv6HostWithPort(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "::1/v1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("[::1]:443", "/v1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcher_BareIPv6NoPort(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "2001:db8::1/v1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("2001:db8::1", "/v1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcher_NoMatch(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("unknown.com", "/anything")
	if h != nil {
		t.Fatalf("expected nil, got %v", h)
	}
}

func TestAccountMatcher_QueryStripped(t *testing.T) {
	m := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["Authorization"]`},
	})

	h := m.Match("api.example.com", "/v1/users?key=val")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("expected [Authorization], got %v", h)
	}
}

func TestAccountMatcherRuntime_Swap(t *testing.T) {
	initial := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["Authorization"]`},
	})
	rt := NewAccountMatcherRuntime(initial)

	h := rt.Match("api.example.com", "/v1/users")
	if len(h) != 1 || h[0] != "Authorization" {
		t.Fatalf("initial match: expected [Authorization], got %v", h)
	}

	next := BuildAccountMatcher([]model.AccountHeaderRule{
		{URLPrefix: "api.example.com/v1", HeadersJSON: `["x-api-key"]`},
	})
	rt.Swap(next)

	h = rt.Match("api.example.com", "/v1/users")
	if len(h) != 1 || h[0] != "x-api-key" {
		t.Fatalf("after swap: expected [x-api-key], got %v", h)
	}
}

func TestAccountMatcherRuntime_ReplaceRules(t *testing.T) {
	rt := NewAccountMatcherRuntime(nil)

	if h := rt.Match("unknown.com", "/"); h != nil {
		t.Fatalf("initial empty matcher: expected nil, got %v", h)
	}

	rt.ReplaceRules([]model.AccountHeaderRule{
		{URLPrefix: "*", HeadersJSON: `["Authorization", "x-api-key"]`},
	})
	h := rt.Match("unknown.com", "/anything")
	if len(h) != 2 || h[0] != "Authorization" || h[1] != "x-api-key" {
		t.Fatalf("after replace rules: expected wildcard headers, got %v", h)
	}
}

func TestExtractAccountFromHeaders_Ordered(t *testing.T) {
	// First non-empty header value wins.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("x-api-key", "account-from-key")

	// Only x-api-key is set; Authorization is missing.
	account := extractAccountFromHeaders(r, []string{"Authorization", "x-api-key"})
	if account != "account-from-key" {
		t.Fatalf("expected account-from-key, got %q", account)
	}
}

func TestExtractAccountFromHeaders_FirstWins(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "account-auth")
	r.Header.Set("x-api-key", "account-key")

	// Both headers present → Authorization (first in list) wins.
	account := extractAccountFromHeaders(r, []string{"Authorization", "x-api-key"})
	if account != "account-auth" {
		t.Fatalf("expected account-auth, got %q", account)
	}
}

func TestExtractAccountFromHeaders_AllEmpty(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	account := extractAccountFromHeaders(r, []string{"Authorization", "x-api-key"})
	if account != "" {
		t.Fatalf("expected empty, got %q", account)
	}
}

func TestExtractAccountFromHeaders_NilHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	account := extractAccountFromHeaders(r, nil)
	if account != "" {
		t.Fatalf("expected empty, got %q", account)
	}
}

func TestClassifyUpstreamError_Timeout(t *testing.T) {
	err := &http.MaxBytesError{} // just need some error
	// Use a real timeout error type.
	type timeoutErr struct{ error }
	func() { _ = timeoutErr{} }()

	// context.DeadlineExceeded
	pe := classifyUpstreamError(deadlineExceededErr{})
	if pe != ErrUpstreamTimeout {
		t.Fatalf("expected ErrUpstreamTimeout, got %v", pe)
	}
	_ = err
}

func TestClassifyUpstreamError_ContextCanceled(t *testing.T) {
	pe := classifyUpstreamError(canceledErr{})
	if pe != nil {
		t.Fatalf("expected nil for context.Canceled, got %v", pe)
	}
}

func TestClassifyUpstreamError_DialError(t *testing.T) {
	// In non-CONNECT paths, dial errors are UPSTREAM_REQUEST_FAILED.
	pe := classifyUpstreamError(dialErr{})
	if pe != ErrUpstreamRequestFailed {
		t.Fatalf("expected ErrUpstreamRequestFailed, got %v", pe)
	}
}

func TestClassifyUpstreamError_GenericError(t *testing.T) {
	pe := classifyUpstreamError(genericErr{})
	if pe != ErrUpstreamRequestFailed {
		t.Fatalf("expected ErrUpstreamRequestFailed, got %v", pe)
	}
}

func TestClassifyUpstreamError_Nil(t *testing.T) {
	pe := classifyUpstreamError(nil)
	if pe != nil {
		t.Fatalf("expected nil, got %v", pe)
	}
}
