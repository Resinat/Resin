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

func TestAPIContract_SubscriptionUpstreamCreateAndPatchValidation(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	createARec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-a",
		"url":  "https://example.com/a",
	}, true)
	if createARec.Code != http.StatusCreated {
		t.Fatalf("create sub-a status: got %d, want %d, body=%s", createARec.Code, http.StatusCreated, createARec.Body.String())
	}
	createABody := decodeJSONMap(t, createARec)
	subAID, _ := createABody["id"].(string)
	if subAID == "" {
		t.Fatalf("create sub-a missing id: body=%s", createARec.Body.String())
	}

	createBRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":                     "sub-b",
		"url":                      "https://example.com/b",
		"upstream_subscription_id": subAID,
	}, true)
	if createBRec.Code != http.StatusCreated {
		t.Fatalf("create sub-b with upstream status: got %d, want %d, body=%s", createBRec.Code, http.StatusCreated, createBRec.Body.String())
	}
	createBBody := decodeJSONMap(t, createBRec)
	subBID, _ := createBBody["id"].(string)
	if subBID == "" {
		t.Fatalf("create sub-b missing id: body=%s", createBRec.Body.String())
	}
	if got, _ := createBBody["upstream_subscription_id"].(string); got != subAID {
		t.Fatalf("create sub-b upstream_subscription_id: got %q, want %q", got, subAID)
	}

	getBRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subBID, nil, true)
	if getBRec.Code != http.StatusOK {
		t.Fatalf("get sub-b status: got %d, want %d, body=%s", getBRec.Code, http.StatusOK, getBRec.Body.String())
	}
	getBBody := decodeJSONMap(t, getBRec)
	if got, _ := getBBody["upstream_subscription_id"].(string); got != subAID {
		t.Fatalf("get sub-b upstream_subscription_id: got %q, want %q", got, subAID)
	}

	selfRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subBID, map[string]any{
		"upstream_subscription_id": subBID,
	}, true)
	if selfRec.Code != http.StatusBadRequest {
		t.Fatalf("patch self upstream status: got %d, want %d, body=%s", selfRec.Code, http.StatusBadRequest, selfRec.Body.String())
	}
	assertErrorCode(t, selfRec, "INVALID_ARGUMENT")

	missingRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subBID, map[string]any{
		"upstream_subscription_id": "11111111-1111-1111-1111-111111111111",
	}, true)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("patch missing upstream status: got %d, want %d, body=%s", missingRec.Code, http.StatusBadRequest, missingRec.Body.String())
	}
	assertErrorCode(t, missingRec, "INVALID_ARGUMENT")

	cycleRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subAID, map[string]any{
		"upstream_subscription_id": subBID,
	}, true)
	if cycleRec.Code != http.StatusBadRequest {
		t.Fatalf("patch cycle upstream status: got %d, want %d, body=%s", cycleRec.Code, http.StatusBadRequest, cycleRec.Body.String())
	}
	assertErrorCode(t, cycleRec, "INVALID_ARGUMENT")

	clearRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subBID, map[string]any{
		"upstream_subscription_id": "",
	}, true)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("patch clear upstream status: got %d, want %d, body=%s", clearRec.Code, http.StatusOK, clearRec.Body.String())
	}
	clearBody := decodeJSONMap(t, clearRec)
	if got, _ := clearBody["upstream_subscription_id"].(string); got != "" {
		t.Fatalf("clear upstream_subscription_id: got %q, want empty", got)
	}
}
