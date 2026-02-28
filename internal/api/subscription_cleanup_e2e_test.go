package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
)

func TestAPIContract_SubscriptionCleanupAction_E2E(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-cleanup-e2e",
		"url":  "https://example.com/sub",
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	createBody := decodeJSONMap(t, createRec)
	subID, _ := createBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}

	sub := cp.SubMgr.Lookup(subID)
	if sub == nil {
		t.Fatalf("subscription %s not found in manager", subID)
	}

	circuitRaw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	circuitHash := node.HashFromRawOptions(circuitRaw)
	cp.Pool.AddNodeFromSub(circuitHash, circuitRaw, subID)
	sub.ManagedNodes().StoreNode(circuitHash, subscription.ManagedNode{Tags: []string{"circuit"}})
	circuitEntry, ok := cp.Pool.GetEntry(circuitHash)
	if !ok {
		t.Fatalf("missing circuit node %s in pool", circuitHash.Hex())
	}
	circuitEntry.CircuitOpenSince.Store(time.Now().Add(-time.Minute).UnixNano())

	noOutboundErrRaw := []byte(`{"type":"ss","server":"2.2.2.2","port":443}`)
	noOutboundErrHash := node.HashFromRawOptions(noOutboundErrRaw)
	cp.Pool.AddNodeFromSub(noOutboundErrHash, noOutboundErrRaw, subID)
	sub.ManagedNodes().StoreNode(noOutboundErrHash, subscription.ManagedNode{Tags: []string{"failed"}})
	noOutboundErrEntry, ok := cp.Pool.GetEntry(noOutboundErrHash)
	if !ok {
		t.Fatalf("missing no-outbound-error node %s in pool", noOutboundErrHash.Hex())
	}
	noOutboundErrEntry.SetLastError("outbound build failed")

	healthyRaw := []byte(`{"type":"ss","server":"3.3.3.3","port":443}`)
	healthyHash := node.HashFromRawOptions(healthyRaw)
	cp.Pool.AddNodeFromSub(healthyHash, healthyRaw, subID)
	sub.ManagedNodes().StoreNode(healthyHash, subscription.ManagedNode{Tags: []string{"healthy"}})
	healthyEntry, ok := cp.Pool.GetEntry(healthyHash)
	if !ok {
		t.Fatalf("missing healthy node %s in pool", healthyHash.Hex())
	}
	outbound := testutil.NewNoopOutbound()
	healthyEntry.Outbound.Store(&outbound)
	healthyEntry.CircuitOpenSince.Store(0)

	beforeCleanupSubRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if beforeCleanupSubRec.Code != http.StatusOK {
		t.Fatalf("get subscription before cleanup status: got %d, want %d, body=%s", beforeCleanupSubRec.Code, http.StatusOK, beforeCleanupSubRec.Body.String())
	}
	beforeCleanupSubBody := decodeJSONMap(t, beforeCleanupSubRec)
	if got := beforeCleanupSubBody["node_count"]; got != float64(3) {
		t.Fatalf("subscription node_count before cleanup: got %v, want 3", got)
	}
	if got := beforeCleanupSubBody["healthy_node_count"]; got != float64(1) {
		t.Fatalf("subscription healthy_node_count before cleanup: got %v, want 1", got)
	}

	cleanupRec := doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/subscriptions/"+subID+"/actions/cleanup-circuit-open-nodes",
		nil,
		true,
	)
	if cleanupRec.Code != http.StatusOK {
		t.Fatalf("cleanup action status: got %d, want %d, body=%s", cleanupRec.Code, http.StatusOK, cleanupRec.Body.String())
	}
	cleanupBody := decodeJSONMap(t, cleanupRec)
	if got := cleanupBody["cleaned_count"]; got != float64(2) {
		t.Fatalf("cleanup cleaned_count: got %v, want 2", got)
	}

	nodesRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/nodes?subscription_id="+subID, nil, true)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("list nodes by subscription status: got %d, want %d, body=%s", nodesRec.Code, http.StatusOK, nodesRec.Body.String())
	}
	nodesBody := decodeJSONMap(t, nodesRec)
	items, ok := nodesBody["items"].([]any)
	if !ok {
		t.Fatalf("items type: got %T", nodesBody["items"])
	}
	if len(items) != 1 {
		t.Fatalf("remaining node count: got %d, want 1, body=%s", len(items), nodesRec.Body.String())
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("node item type: got %T", items[0])
	}
	if got, _ := item["node_hash"].(string); got != healthyHash.Hex() {
		t.Fatalf("remaining node hash: got %q, want %q", got, healthyHash.Hex())
	}

	afterCleanupSubRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if afterCleanupSubRec.Code != http.StatusOK {
		t.Fatalf("get subscription after cleanup status: got %d, want %d, body=%s", afterCleanupSubRec.Code, http.StatusOK, afterCleanupSubRec.Body.String())
	}
	afterCleanupSubBody := decodeJSONMap(t, afterCleanupSubRec)
	if got := afterCleanupSubBody["node_count"]; got != float64(1) {
		t.Fatalf("subscription node_count after cleanup: got %v, want 1", got)
	}
	if got := afterCleanupSubBody["healthy_node_count"]; got != float64(1) {
		t.Fatalf("subscription healthy_node_count after cleanup: got %v, want 1", got)
	}

	secondCleanupRec := doJSONRequest(
		t,
		srv,
		http.MethodPost,
		"/api/v1/subscriptions/"+subID+"/actions/cleanup-circuit-open-nodes",
		nil,
		true,
	)
	if secondCleanupRec.Code != http.StatusOK {
		t.Fatalf("second cleanup action status: got %d, want %d, body=%s", secondCleanupRec.Code, http.StatusOK, secondCleanupRec.Body.String())
	}
	secondBody := decodeJSONMap(t, secondCleanupRec)
	if got := secondBody["cleaned_count"]; got != float64(0) {
		t.Fatalf("second cleanup cleaned_count: got %v, want 0", got)
	}
}
