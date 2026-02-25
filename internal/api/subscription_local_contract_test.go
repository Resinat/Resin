package api

import (
	"net/http"
	"testing"
)

func TestAPIContract_SubscriptionLocalCreateValidation(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-local",
		"source_type": "local",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create local without content status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-local",
		"source_type": "local",
		"content":     "vmess://example",
		"url":         "https://example.com/sub",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create local with url status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_SubscriptionSourceTypeReadOnlyOnPatch(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-remote",
		"url":  "https://example.com/sub",
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create remote subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	body := decodeJSONMap(t, createRec)
	subID, _ := body["id"].(string)
	if subID == "" {
		t.Fatalf("create remote subscription missing id: body=%s", createRec.Body.String())
	}

	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"source_type": "local",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch source_type status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}
