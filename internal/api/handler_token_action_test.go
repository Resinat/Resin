package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
)

func doTokenJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	var err error
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTokenActionInheritLease_Success(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-target"
	platformID := mustCreatePlatform(t, srv, platformName)

	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		Account:        "parent-account",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    nowNs - int64(10*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent-account",
			"new_account":    "new-account",
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	if body["status"] != "ok" {
		t.Fatalf("status field: got %v, want %q", body["status"], "ok")
	}

	child := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "new-account"})
	if child == nil {
		t.Fatal("expected new-account lease to be created")
	}
	if child.NodeHash != parent.NodeHash {
		t.Fatalf("child node_hash: got %q, want %q", child.NodeHash, parent.NodeHash)
	}
	if child.EgressIP != parent.EgressIP {
		t.Fatalf("child egress_ip: got %q, want %q", child.EgressIP, parent.EgressIP)
	}
	if child.ExpiryNs != parent.ExpiryNs {
		t.Fatalf("child expiry_ns: got %d, want %d", child.ExpiryNs, parent.ExpiryNs)
	}
}

func TestTokenActionInheritLease_RejectsUnknownFields(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-unknown-field"
	_ = mustCreatePlatform(t, srv, platformName)

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent",
			"new_account":    "child",
			"extra":          "unexpected",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestTokenActionInheritLease_ParentMissingOrExpiredReturnsNotFound(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-parent-notfound"
	platformID := mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "missing-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	nowNs := time.Now().UnixNano()
	expired := model.Lease{
		PlatformID:     platformID,
		Account:        "expired-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"expired-token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.22",
		CreatedAtNs:    nowNs - int64(2*time.Hour),
		ExpiryNs:       nowNs - int64(time.Second),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(expired); err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}

	rec = doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "expired-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expired parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")
}

func TestTokenActionInheritLease_InvalidArguments(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-invalid-args"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "same-account",
			"new_account":    "same-account",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same account status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}
