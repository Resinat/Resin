package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestAPIContract_SubscriptionCreateRemote_UsesURLsArrayAndKeepsBaseNameForSingle(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "single-sub",
		"urls": []string{"https://api.example.com/sub"},
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create single remote by urls status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	itemsRaw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("create response items type: got %T, body=%s", body["items"], rec.Body.String())
	}
	if len(itemsRaw) != 1 {
		t.Fatalf("create response items len: got %d, want %d, body=%s", len(itemsRaw), 1, rec.Body.String())
	}
	item, ok := itemsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("create response item type: got %T", itemsRaw[0])
	}
	if got, _ := item["name"].(string); got != "single-sub" {
		t.Fatalf("single create name: got %q, want %q", got, "single-sub")
	}
	if got, _ := item["url"].(string); got != "https://api.example.com/sub" {
		t.Fatalf("single create url: got %q, want %q", got, "https://api.example.com/sub")
	}
}

func TestAPIContract_SubscriptionCreateRemote_MultipleURLsAppendDomainSuffix(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "batch-sub",
		"urls": []string{
			"https://a.example.com/sub-1",
			"https://api.google.co.uk/sub-2",
			"https://b.example.com/sub-3",
		},
	}, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create multiple remote by urls status: got %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	itemsRaw, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("create response items type: got %T, body=%s", body["items"], rec.Body.String())
	}
	if len(itemsRaw) != 3 {
		t.Fatalf("create response items len: got %d, want %d, body=%s", len(itemsRaw), 3, rec.Body.String())
	}

	wantNames := []string{"batch-sub-example.com", "batch-sub-google.co.uk", "batch-sub-example.com-2"}
	for i := range itemsRaw {
		item, ok := itemsRaw[i].(map[string]any)
		if !ok {
			t.Fatalf("create response items[%d] type: got %T", i, itemsRaw[i])
		}
		if got, _ := item["name"].(string); got != wantNames[i] {
			t.Fatalf("create response items[%d].name: got %q, want %q", i, got, wantNames[i])
		}
	}
}

func TestAPIContract_SubscriptionCreateRemote_URLFieldRejectedAndInvalidBatchIsAtomic(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	urlFieldRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "legacy-url",
		"url":  "https://example.com/sub",
	}, true)
	if urlFieldRec.Code != http.StatusBadRequest {
		t.Fatalf("create remote with url field status: got %d, want %d, body=%s", urlFieldRec.Code, http.StatusBadRequest, urlFieldRec.Body.String())
	}
	assertErrorCode(t, urlFieldRec, "INVALID_ARGUMENT")

	invalidBatchRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "atomic-batch",
		"urls": []string{
			"https://ok.example.com/sub-1",
			"not-a-url",
			"https://ok2.example.com/sub-2",
		},
	}, true)
	if invalidBatchRec.Code != http.StatusBadRequest {
		t.Fatalf("create invalid batch status: got %d, want %d, body=%s", invalidBatchRec.Code, http.StatusBadRequest, invalidBatchRec.Body.String())
	}
	assertErrorCode(t, invalidBatchRec, "INVALID_ARGUMENT")
	if !strings.Contains(invalidBatchRec.Body.String(), "urls[1]") {
		t.Fatalf("invalid batch error should contain urls index, body=%s", invalidBatchRec.Body.String())
	}

	listRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions", nil, true)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list subscriptions status: got %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}
	listBody := decodeJSONMap(t, listRec)
	itemsRaw, ok := listBody["items"].([]any)
	if !ok {
		t.Fatalf("list items type: got %T, body=%s", listBody["items"], listRec.Body.String())
	}
	if len(itemsRaw) != 0 {
		t.Fatalf("invalid batch should be atomic and create nothing, got items=%d body=%s", len(itemsRaw), listRec.Body.String())
	}
}
