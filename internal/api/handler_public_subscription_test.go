package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/service"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newPublicSubscriptionHandlerTestService(t *testing.T) (*service.ControlPlaneService, *subscription.Subscription) {
	t.Helper()
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 4,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return time.Minute },
	})
	sub := subscription.NewSubscription("11111111-1111-1111-1111-111111111111", "public", "", true, false)
	sub.SetPublicToken("token")
	subMgr.Register(sub)

	raw := []byte(`{"type":"shadowsocks","tag":"node","server":"example.com","server_port":443,"method":"aes-128-gcm","password":"pass"}`)
	h := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(h, raw, sub.ID)
	sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{"node"}})
	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("node missing")
	}
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.CircuitOpenSince.Store(0)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.1"))
	entry.LatencyTable.Update("example.com", 10*time.Millisecond, time.Minute)
	sub.SetUsage(subscription.UsageInfo{UploadBytes: 1, DownloadBytes: 2, TotalBytes: 3, ExpireUnix: 4, UpdatedAtNs: 1})

	return &service.ControlPlaneService{Pool: pool, SubMgr: subMgr}, sub
}

func TestPublicSubscriptionHandler_ServesRequestedFormatAndUsage(t *testing.T) {
	cp, sub := newPublicSubscriptionHandlerTestService(t)
	req := httptest.NewRequest(http.MethodGet, "/sub/"+sub.ID+"/token?format=sing-box", nil)
	rec := httptest.NewRecorder()
	NewPublicSubscriptionHandler(cp).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Profile-Title"); got != sub.Name() {
		t.Fatalf("Profile-Title = %q, want %q", got, sub.Name())
	}
	if got := rec.Header().Get("Profile-Update-Interval"); got != "24" {
		t.Fatalf("Profile-Update-Interval = %q, want 24", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "attachment; filename*=UTF-8''public" {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Subscription-Userinfo"); got != "upload=1; download=2; total=3; expire=4" {
		t.Fatalf("Subscription-Userinfo = %q", got)
	}
	var body struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1", len(body.Outbounds))
	}
}

func TestPublicSubscriptionHandler_AutoDetectsClientFormat(t *testing.T) {
	cp, sub := newPublicSubscriptionHandlerTestService(t)
	handler := NewPublicSubscriptionHandler(cp)
	tests := []struct {
		name      string
		userAgent string
		path      string
		check     func(*testing.T, []byte)
	}{
		{
			name:      "v2rayN",
			userAgent: "v2rayN/7.0",
			path:      "/sub/" + sub.ID + "/token",
			check: func(t *testing.T, body []byte) {
				decoded, err := base64.StdEncoding.DecodeString(string(body))
				if err != nil {
					t.Fatalf("decode v2ray output: %v", err)
				}
				if !strings.HasPrefix(string(decoded), "ss://") {
					t.Fatalf("decoded output = %q", decoded)
				}
			},
		},
		{
			name:      "sing-box",
			userAgent: "sing-box/1.0",
			path:      "/sub/" + sub.ID + "/token",
			check: func(t *testing.T, body []byte) {
				var value struct {
					Outbounds []json.RawMessage `json:"outbounds"`
				}
				if err := json.Unmarshal(body, &value); err != nil {
					t.Fatalf("decode sing-box output: %v", err)
				}
				if len(value.Outbounds) != 1 {
					t.Fatalf("outbounds = %d, want 1", len(value.Outbounds))
				}
			},
		},
		{
			name:      "mihomo fallback",
			userAgent: "Mihomo/1.0",
			path:      "/sub/" + sub.ID + "/token",
			check: func(t *testing.T, body []byte) {
				if !strings.Contains(string(body), "proxy-groups:") {
					t.Fatalf("expected clash configuration, got %s", body)
				}
			},
		},
		{
			name:      "explicit auto",
			userAgent: "unknown-client",
			path:      "/sub/" + sub.ID + "/token?format=auto",
			check: func(t *testing.T, body []byte) {
				if !strings.Contains(string(body), "proxy-groups:") {
					t.Fatalf("expected clash configuration, got %s", body)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("User-Agent", tt.userAgent)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			tt.check(t, rec.Body.Bytes())
		})
	}
}

func TestPublicSubscriptionHandler_RejectsInvalidFormatAndToken(t *testing.T) {
	cp, sub := newPublicSubscriptionHandlerTestService(t)
	handler := NewPublicSubscriptionHandler(cp)

	for _, path := range []string{
		"/sub/" + sub.ID + "/token?format=unknown",
		"/sub/" + sub.ID + "/wrong?format=sing-box",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Fatalf("path %q status = %d, want 400 or 404", path, rec.Code)
		}
	}
}
