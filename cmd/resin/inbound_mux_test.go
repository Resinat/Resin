package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func tagHandler(tag string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", tag)
		w.WriteHeader(status)
	})
}

func TestInboundMux_PriorityForwardConnect(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodConnect, "http://example.com:443", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Header().Get("X-Route") != "forward" {
		t.Fatalf("expected forward route, got %q", rec.Header().Get("X-Route"))
	}
}

func TestInboundMux_PriorityForwardAbsoluteURI(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/v1/ping", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Header().Get("X-Route") != "forward" {
		t.Fatalf("expected forward route, got %q", rec.Header().Get("X-Route"))
	}
}

func TestInboundMux_RoutesTokenAPINamespaceToTokenActionHandler(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	for _, path := range []string{"/tok/api", "/tok/api/v1/platforms"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Header().Get("X-Route") != "token-action" {
				t.Fatalf("expected token-action route, got %q", rec.Header().Get("X-Route"))
			}
		})
	}
}

func TestInboundMux_RoutesAPIForControlPlanePaths(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	cases := []string{
		"/",
		"/healthz",
		"/api",
		"/api/v1/system/info",
		"/ui",
		"/ui/",
		"/ui/platforms/demo",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Header().Get("X-Route") != "api" {
				t.Fatalf("expected api route, got %q", rec.Header().Get("X-Route"))
			}
		})
	}
}

func TestInboundMux_RoutesReverseForNonControlPlanePaths(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	cases := []string{
		"/tok/plat:acct/https/example.com/path",
		"/tok/plat/https/example.com/path",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Header().Get("X-Route") != "reverse" {
				t.Fatalf("expected reverse route, got %q", rec.Header().Get("X-Route"))
			}
		})
	}
}

func TestInboundMux_RejectsReverseWhenTokenMissingOrWrong(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	cases := []string{
		"/dashboard",
		"/wrong/plat:acct/https/example.com/path",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusForbidden)
			}
			if got := rec.Header().Get("X-Resin-Error"); got != "AUTH_FAILED" {
				t.Fatalf("X-Resin-Error: got %q, want %q", got, "AUTH_FAILED")
			}
			if got := rec.Header().Get("X-Route"); got != "" {
				t.Fatalf("expected no downstream handler route, got %q", got)
			}
		})
	}
}

func TestInboundMux_EmptyProxyTokenDoesNotReserveTokenAPI(t *testing.T) {
	mux := newInboundMux(
		"",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodGet, "/any-token/api/v1/system/info", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Header().Get("X-Route") != "reverse" {
		t.Fatalf("expected reverse route when proxy token empty, got %q", rec.Header().Get("X-Route"))
	}
}

func TestInboundMux_RoutesTokenInheritLeaseAction(t *testing.T) {
	mux := newInboundMux(
		"tok",
		tagHandler("forward", http.StatusOK),
		tagHandler("reverse", http.StatusOK),
		tagHandler("api", http.StatusOK),
		tagHandler("token-action", http.StatusOK),
	)

	req := httptest.NewRequest(http.MethodPost, "/tok/api/v1/Default/actions/inherit-lease", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Header().Get("X-Route") != "token-action" {
		t.Fatalf("expected token-action route, got %q", rec.Header().Get("X-Route"))
	}
}
