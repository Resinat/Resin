package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSingboxSubscription_Basic(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "ss-us", "server": "1.2.3.4", "server_port": 443},
			{"type": "vmess", "tag": "vmess-jp", "server": "5.6.7.8", "server_port": 443},
			{"type": "direct", "tag": "direct"},
			{"type": "block", "tag": "block"},
			{"type": "selector", "tag": "proxy", "outbounds": ["ss-us", "vmess-jp"]}
		]
	}`)

	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatal(err)
	}

	// Only shadowsocks and vmess are supported; direct/block/selector are not.
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	if nodes[0].Tag != "ss-us" {
		t.Fatalf("expected tag ss-us, got %s", nodes[0].Tag)
	}
	if nodes[1].Tag != "vmess-jp" {
		t.Fatalf("expected tag vmess-jp, got %s", nodes[1].Tag)
	}
}

func TestParseSingboxSubscription_AllSupportedTypes(t *testing.T) {
	types := []string{
		"socks", "http", "shadowsocks", "vmess", "trojan", "wireguard",
		"hysteria", "vless", "shadowtls", "tuic", "hysteria2", "anytls",
		"tor", "ssh", "naive",
	}

	// Build JSON with all supported types.
	outbounds := "["
	for i, tp := range types {
		if i > 0 {
			outbounds += ","
		}
		outbounds += `{"type":"` + tp + `","tag":"node-` + tp + `"}`
	}
	outbounds += "]"

	data := []byte(`{"outbounds":` + outbounds + `}`)

	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != len(types) {
		t.Fatalf("expected %d nodes, got %d", len(types), len(nodes))
	}
}

func TestParseSingboxSubscription_UnsupportedTypesFiltered(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "direct", "tag": "direct"},
			{"type": "block", "tag": "block"},
			{"type": "selector", "tag": "sel"},
			{"type": "urltest", "tag": "urltest"},
			{"type": "dns", "tag": "dns"}
		]
	}`)

	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestParseSingboxSubscription_EmptyOutbounds(t *testing.T) {
	data := []byte(`{"outbounds": []}`)
	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestParseSingboxSubscription_MalformedJSON(t *testing.T) {
	_, err := ParseSingboxSubscription([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseSingboxSubscription_MalformedOutboundSkipped(t *testing.T) {
	// A bare number is not a valid JSON object for an outbound — should be skipped.
	data := []byte(`{"outbounds": [123]}`)
	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatalf("malformed individual outbound should be skipped, not fatal: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after skipping bad entry, got %d", len(nodes))
	}
}

func TestParseSingboxSubscription_MixedGoodAndBadOutbounds(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "good-node", "server": "1.2.3.4", "server_port": 443},
			123,
			"bad-string",
			{"type": "vmess", "tag": "also-good", "server": "5.6.7.8", "server_port": 443}
		]
	}`)
	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatalf("should skip bad entries, not fail: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 valid nodes, got %d", len(nodes))
	}
	if nodes[0].Tag != "good-node" || nodes[1].Tag != "also-good" {
		t.Fatalf("unexpected tags: %s, %s", nodes[0].Tag, nodes[1].Tag)
	}
}

func TestParseSingboxSubscription_RawOptionsPreservesFullJSON(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "ss", "server": "1.2.3.4", "server_port": 443, "method": "aes-256-gcm"}
		]
	}`)

	nodes, err := ParseSingboxSubscription(data)
	if err != nil {
		t.Fatal(err)
	}

	// RawOptions should contain the full original JSON.
	raw := string(nodes[0].RawOptions)
	if len(raw) == 0 {
		t.Fatal("RawOptions should not be empty")
	}
	// Should contain method field.
	if !strings.Contains(raw, "aes-256-gcm") {
		t.Fatalf("RawOptions missing method: %s", raw)
	}
}

func TestParseGeneralSubscription_ClashJSON(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "ss-test",
				"type": "ss",
				"server": "1.1.1.1",
				"port": 8388,
				"cipher": "aes-128-gcm",
				"password": "pass"
			},
			{
				"name": "ignored-http",
				"type": "http",
				"server": "2.2.2.2",
				"port": 8080
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "shadowsocks" {
		t.Fatalf("expected type shadowsocks, got %v", got)
	}
	if got := obj["tag"]; got != "ss-test" {
		t.Fatalf("expected tag ss-test, got %v", got)
	}
}

func TestParseGeneralSubscription_ClashYAML(t *testing.T) {
	data := []byte(`
proxies:
  - name: vmess-yaml
    type: vmess
    server: 3.3.3.3
    port: 443
    uuid: 26a1d547-b031-4139-9fc5-6671e1d0408a
    cipher: auto
    tls: true
    servername: example.com
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "vmess" {
		t.Fatalf("expected type vmess, got %v", got)
	}
	if got := obj["tag"]; got != "vmess-yaml" {
		t.Fatalf("expected tag vmess-yaml, got %v", got)
	}
}

func TestParseGeneralSubscription_URILines(t *testing.T) {
	data := []byte(`
trojan://password@example.com:443?allowInsecure=1&type=ws&sni=example.com#Trojan%20Node
vless://26a1d547-b031-4139-9fc5-6671e1d0408a@example.com:443?type=tcp&security=tls&sni=example.com#VLESS%20Node
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	first := parseNodeRaw(t, nodes[0].RawOptions)
	second := parseNodeRaw(t, nodes[1].RawOptions)
	if first["type"] != "trojan" || second["type"] != "vless" {
		t.Fatalf("unexpected node types: %v, %v", first["type"], second["type"])
	}
}

func TestParseGeneralSubscription_Base64WrappedURIs(t *testing.T) {
	plain := "ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388#SS-Node"
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))

	nodes, err := ParseGeneralSubscription([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "shadowsocks" {
		t.Fatalf("expected type shadowsocks, got %v", got)
	}
	if got := obj["tag"]; got != "SS-Node" {
		t.Fatalf("expected tag SS-Node, got %v", got)
	}
}

func TestParseGeneralSubscription_UnknownFormatReturnsError(t *testing.T) {
	_, err := ParseGeneralSubscription([]byte("this is not a subscription format"))
	if err == nil {
		t.Fatal("expected error for unknown subscription format")
	}
}

func parseNodeRaw(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal node raw failed: %v", err)
	}
	return obj
}
