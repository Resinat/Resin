package service

import (
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
	"gopkg.in/yaml.v3"
)

func newPublicSubscriptionTestService(t *testing.T) (*ControlPlaneService, *subscription.Subscription, node.Hash) {
	t.Helper()
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	sub := subscription.NewSubscription("sub-public", "Public", "https://example.com/sub", true, false)
	sub.SetPublicToken("public-token")
	subMgr.Register(sub)

	raw := []byte(`{"type":"vmess","tag":"old-name","server":"example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","security":"auto"}`)
	h := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(h, raw, sub.ID)
	sub.ManagedNodes().StoreNode(h, subscription.ManagedNode{Tags: []string{"vmess-node"}})
	entry, ok := pool.GetEntry(h)
	if !ok {
		t.Fatal("node missing")
	}
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.CircuitOpenSince.Store(0)
	entry.SetEgressIP(netip.MustParseAddr("203.0.113.10"))
	entry.LatencyTable.Update("example.com", 20*time.Millisecond, time.Minute)

	return &ControlPlaneService{Pool: pool, SubMgr: subMgr}, sub, h
}

func TestRenderPublicSubscription_SingBoxKeepsHealthyNodeAndSkipsUnhealthy(t *testing.T) {
	cp, sub, healthyHash := newPublicSubscriptionTestService(t)
	sub.SetUsage(subscription.UsageInfo{
		UploadBytes:   10,
		DownloadBytes: 20,
		TotalBytes:    100,
		ExpireUnix:    1893456000,
		UpdatedAtNs:   1,
	})

	rendered, err := cp.RenderPublicSubscription(sub.ID, "public-token", PublicSubscriptionFormatSingBox)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(rendered.Body, &body); err != nil {
		t.Fatalf("decode sing-box output: %v", err)
	}
	if len(body.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1", len(body.Outbounds))
	}
	if body.Outbounds[0]["tag"] != "vmess-node" {
		t.Fatalf("tag = %v, want vmess-node", body.Outbounds[0]["tag"])
	}
	if healthyHash.IsZero() {
		t.Fatal("healthy hash unexpectedly zero")
	}
}

func TestRenderPublicSubscription_RejectsInvalidToken(t *testing.T) {
	cp, sub, _ := newPublicSubscriptionTestService(t)
	if _, err := cp.RenderPublicSubscription(sub.ID, "wrong", PublicSubscriptionFormatSingBox); err == nil {
		t.Fatal("expected invalid token error")
	}
}

func TestRenderPublicSubscription_FormatsAndUsage(t *testing.T) {
	cp, sub, _ := newPublicSubscriptionTestService(t)
	sub.SetUsage(subscription.UsageInfo{
		UploadBytes:   1,
		DownloadBytes: 2,
		TotalBytes:    3,
		ExpireUnix:    4,
		UpdatedAtNs:   1,
	})

	for _, format := range []string{PublicSubscriptionFormatClash, PublicSubscriptionFormatV2Ray, PublicSubscriptionFormatSingBox} {
		t.Run(format, func(t *testing.T) {
			result, err := cp.RenderPublicSubscription(sub.ID, sub.PublicToken(), format)
			if err != nil {
				t.Fatal(err)
			}
			if result.ContentType == "" || len(result.Body) == 0 {
				t.Fatalf("empty result: %+v", result)
			}
			if got := subscription.FormatSubscriptionUserinfo(result.Usage); got != "upload=1; download=2; total=3; expire=4" {
				t.Fatalf("userinfo = %q", got)
			}
			if format == PublicSubscriptionFormatV2Ray {
				decoded, err := base64.StdEncoding.DecodeString(string(result.Body))
				if err != nil {
					t.Fatalf("decode v2ray output: %v", err)
				}
				if !strings.HasPrefix(string(decoded), "vmess://") {
					t.Fatalf("decoded v2ray output = %q", decoded)
				}
			}
		})
	}
}

func TestRenderPublicSubscription_ClashIncludesBaseConfigAndExpandedProxyGroups(t *testing.T) {
	cp, sub, _ := newPublicSubscriptionTestService(t)
	result, err := cp.RenderPublicSubscription(sub.ID, sub.PublicToken(), PublicSubscriptionFormatClash)
	if err != nil {
		t.Fatal(err)
	}

	var body struct {
		Port               int    `yaml:"port"`
		SocksPort          int    `yaml:"socks-port"`
		AllowLAN           bool   `yaml:"allow-lan"`
		Mode               string `yaml:"mode"`
		LogLevel           string `yaml:"log-level"`
		ExternalController string `yaml:"external-controller"`
		DNS                struct {
			Enabled    bool     `yaml:"enabled"`
			Nameserver []string `yaml:"nameserver"`
			Fallback   []string `yaml:"fallback"`
		} `yaml:"dns"`
		Proxies     []map[string]any `yaml:"proxies"`
		ProxyGroups []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
		Rules []string `yaml:"rules"`
	}
	if err := yaml.Unmarshal(result.Body, &body); err != nil {
		t.Fatalf("decode clash output: %v\n%s", err, result.Body)
	}
	if body.Port != 7890 || body.SocksPort != 7891 || !body.AllowLAN || body.Mode != "Rule" || body.LogLevel != "info" || body.ExternalController != ":9090" {
		t.Fatalf("base config = %+v", body)
	}
	if !body.DNS.Enabled || len(body.DNS.Nameserver) != 2 || len(body.DNS.Fallback) != 4 {
		t.Fatalf("dns config = %+v", body.DNS)
	}
	if len(body.Proxies) != 1 || body.Proxies[0]["name"] != "vmess-node" {
		t.Fatalf("proxies = %+v", body.Proxies)
	}
	if len(body.ProxyGroups) != 5 {
		t.Fatalf("proxy groups = %+v", body.ProxyGroups)
	}
	for _, group := range body.ProxyGroups {
		if group.Name == "🚀 节点选择" || group.Name == "♻️ 自动选择" || group.Name == "🐟 漏网之鱼" {
			if !containsString(group.Proxies, "vmess-node") {
				t.Fatalf("group %q does not include the proxy: %+v", group.Name, group.Proxies)
			}
		}
	}
	if len(body.Rules) != 2 || body.Rules[0] != "GEOIP,CN,🎯 全球直连" || body.Rules[1] != "MATCH,🐟 漏网之鱼" {
		t.Fatalf("rules = %+v", body.Rules)
	}
	if strings.Contains(string(result.Body), "__ALL_PROXIES__") {
		t.Fatalf("clash output still contains the proxy placeholder: %s", result.Body)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestV2RayURIFromRaw_UsesV2RayNShareSchemes(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		prefix string
	}{
		{
			name:   "socks",
			raw:    `{"type":"socks","server":"example.com","server_port":1080,"username":"u","password":"p"}`,
			prefix: "socks://",
		},
		{
			name:   "hysteria2",
			raw:    `{"type":"hysteria2","server":"example.com","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"sni.example.com"}}`,
			prefix: "hysteria2://",
		},
		{
			name:   "tuic",
			raw:    `{"type":"tuic","server":"example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555","password":"p"}`,
			prefix: "tuic://",
		},
		{
			name:   "wireguard",
			raw:    `{"type":"wireguard","server":"example.com","server_port":443,"private_key":"private","peer_public_key":"public","local_address":["10.0.0.2/32"]}`,
			prefix: "wireguard://",
		},
		{
			name:   "anytls",
			raw:    `{"type":"anytls","server":"example.com","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"sni.example.com"}}`,
			prefix: "anytls://",
		},
		{
			name:   "naive",
			raw:    `{"type":"naive","server":"example.com","server_port":443,"username":"u","password":"p"}`,
			prefix: "naive+https://",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := v2RayURIFromRaw(json.RawMessage(tt.raw), tt.name)
			if !ok {
				t.Fatal("expected URI conversion")
			}
			if !strings.HasPrefix(got, tt.prefix) {
				t.Fatalf("URI = %q, want prefix %q", got, tt.prefix)
			}
		})
	}
}
