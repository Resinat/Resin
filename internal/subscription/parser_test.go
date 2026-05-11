package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestParseGeneralSubscription_SingboxJSON_Basic(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "ss-us", "server": "1.2.3.4", "server_port": 443},
			{"type": "vmess", "tag": "vmess-jp", "server": "5.6.7.8", "server_port": 443},
			{"type": "direct", "tag": "direct"},
			{"type": "block", "tag": "block"},
			{"type": "selector", "tag": "proxy", "outbounds": ["ss-us", "vmess-jp"]}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
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

func TestParseGeneralSubscription_SingboxJSON_AllSupportedTypes(t *testing.T) {
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

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != len(types) {
		t.Fatalf("expected %d nodes, got %d", len(types), len(nodes))
	}
}

func TestParseGeneralSubscription_SingboxJSON_UnsupportedTypesFiltered(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "direct", "tag": "direct"},
			{"type": "block", "tag": "block"},
			{"type": "selector", "tag": "sel"},
			{"type": "urltest", "tag": "urltest"},
			{"type": "dns", "tag": "dns"}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestParseGeneralSubscription_SingboxJSON_EmptyOutbounds(t *testing.T) {
	data := []byte(`{"outbounds": []}`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestParseGeneralSubscription_SingboxJSON_MalformedJSON(t *testing.T) {
	_, err := ParseGeneralSubscription([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestParseGeneralSubscription_SingboxJSON_MalformedOutboundSkipped(t *testing.T) {
	// A bare number is not a valid JSON object for an outbound — should be skipped.
	data := []byte(`{"outbounds": [123]}`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatalf("malformed individual outbound should be skipped, not fatal: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after skipping bad entry, got %d", len(nodes))
	}
}

func TestParseGeneralSubscription_SingboxJSON_MixedGoodAndBadOutbounds(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "good-node", "server": "1.2.3.4", "server_port": 443},
			123,
			"bad-string",
			{"type": "vmess", "tag": "also-good", "server": "5.6.7.8", "server_port": 443}
		]
	}`)
	nodes, err := ParseGeneralSubscription(data)
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

func TestParseGeneralSubscription_SingboxJSON_RawOptionsPreservesFullJSON(t *testing.T) {
	data := []byte(`{
		"outbounds": [
			{"type": "shadowsocks", "tag": "ss", "server": "1.2.3.4", "server_port": 443, "method": "aes-256-gcm"}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
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
				"name": "http-test",
				"type": "http",
				"server": "2.2.2.2",
				"port": 8080,
				"username": "user-http",
				"password": "pass-http"
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	first := parseNodeRaw(t, nodes[0].RawOptions)
	second := parseNodeRaw(t, nodes[1].RawOptions)
	if got := first["type"]; got != "shadowsocks" {
		t.Fatalf("expected type shadowsocks, got %v", got)
	}
	if got := first["tag"]; got != "ss-test" {
		t.Fatalf("expected tag ss-test, got %v", got)
	}
	if got := second["type"]; got != "http" {
		t.Fatalf("expected type http, got %v", got)
	}
	if got := second["tag"]; got != "http-test" {
		t.Fatalf("expected tag http-test, got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_SSPluginOptions(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "ss-plugin",
				"type": "ss",
				"server": "1.1.1.1",
				"port": 8388,
				"cipher": "aes-128-gcm",
				"password": "pass",
				"plugin": "v2ray-plugin",
				"plugin-opts": {
					"mode": "websocket",
					"host": "ws.example.com",
					"path": "/ws",
					"tls": true
				}
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
	if got := obj["plugin"]; got != "v2ray-plugin" {
		t.Fatalf("plugin: got %v", got)
	}
	opts, _ := obj["plugin_opts"].(string)
	if !strings.Contains(opts, "mode=websocket") {
		t.Fatalf("plugin_opts missing mode: %q", opts)
	}
	if !strings.Contains(opts, "host=ws.example.com") {
		t.Fatalf("plugin_opts missing host: %q", opts)
	}
	if !strings.Contains(opts, "path=/ws") {
		t.Fatalf("plugin_opts missing path: %q", opts)
	}
	if !strings.Contains(opts, "tls") {
		t.Fatalf("plugin_opts missing tls flag: %q", opts)
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

func TestParseGeneralSubscription_ClashJSON_NewProtocolsAndDialFields(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "socks-test",
				"type": "socks5",
				"server": "1.1.1.1",
				"port": 1080,
				"username": "socks-user",
				"password": "socks-pass",
				"udp": false,
				"dialer-proxy": "detour-a",
				"bind-interface": "eth0",
				"routing-mark": "0x20",
				"fast-open": true,
				"mptcp": true,
				"udp-fragment": true,
				"ip-version": "ipv6"
			},
			{
				"name": "http-test",
				"type": "http",
				"server": "2.2.2.2",
				"port": 443,
				"username": "http-user",
				"password": "http-pass",
				"headers": {"x-token": "abc"},
				"tls": true,
				"sni": "custom.com",
				"skip-cert-verify": true
			},
			{
				"name": "wg-test",
				"type": "wireguard",
				"server": "162.159.192.1",
				"port": 2480,
				"private-key": "priv-key",
				"public-key": "pub-key",
				"pre-shared-key": "psk",
				"ip": "172.16.0.2",
				"ipv6": "fd01::1",
				"allowed-ips": ["0.0.0.0/0", "::/0"],
				"reserved": [209, 98, 59],
				"mtu": 1408,
				"udp": false,
				"ip-version": "prefer-ipv4"
			},
			{
				"name": "hy-test",
				"type": "hysteria",
				"server": "server.com",
				"port": 443,
				"auth-str": "yourpassword",
				"obfs": "obfs-str",
				"up": "30",
				"down": "200",
				"ports": "1000,2000-3000",
				"protocol": "udp",
				"recv-window-conn": 12582912,
				"recv-window": 52428800,
				"disable_mtu_discovery": true,
				"sni": "server.com",
				"skip-cert-verify": true,
				"alpn": ["h3"]
			},
			{
				"name": "tuic-test",
				"type": "tuic",
				"server": "www.example.com",
				"port": 10443,
				"uuid": "00000000-0000-0000-0000-000000000001",
				"password": "PASSWORD_1",
				"congestion-controller": "bbr",
				"udp-relay-mode": "native",
				"reduce-rtt": true,
				"heartbeat-interval": 10000,
				"disable-sni": true,
				"sni": "example.com",
				"skip-cert-verify": true,
				"alpn": ["h3"]
			},
			{
				"name": "anytls-test",
				"type": "anytls",
				"server": "1.2.3.4",
				"port": 443,
				"password": "anytls-pass",
				"idle-session-check-interval": 30,
				"idle-session-timeout": 40,
				"min-idle-session": 2,
				"sni": "example.com",
				"skip-cert-verify": true,
				"alpn": ["h2", "http/1.1"],
				"client-fingerprint": "chrome"
			},
			{
				"name": "ssh-test",
				"type": "ssh",
				"server": "127.0.0.1",
				"port": 22,
				"username": "root",
				"password": "password",
				"private-key": "key",
				"private-key-passphrase": "key-password",
				"host-key": ["ssh-rsa AAAAB3Nza..."],
				"host-key-algorithms": ["rsa"],
				"client-version": "SSH-2.0-OpenSSH_7.4p1"
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 7 {
		t.Fatalf("expected 7 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)

	socks := byTag["socks-test"]
	if got := socks["type"]; got != "socks" {
		t.Fatalf("socks type: got %v", got)
	}
	if got := socks["version"]; got != "5" {
		t.Fatalf("socks version: got %v", got)
	}
	if got := socks["network"]; got != "tcp" {
		t.Fatalf("socks network: got %v", got)
	}
	if got := socks["detour"]; got != "detour-a" {
		t.Fatalf("socks detour: got %v", got)
	}
	if got := socks["bind_interface"]; got != "eth0" {
		t.Fatalf("socks bind_interface: got %v", got)
	}
	if got := socks["routing_mark"]; got != "0x20" {
		t.Fatalf("socks routing_mark: got %v", got)
	}
	if got := socks["tcp_fast_open"]; got != true {
		t.Fatalf("socks tcp_fast_open: got %v", got)
	}
	if got := socks["tcp_multi_path"]; got != true {
		t.Fatalf("socks tcp_multi_path: got %v", got)
	}
	if got := socks["udp_fragment"]; got != true {
		t.Fatalf("socks udp_fragment: got %v", got)
	}
	if got := socks["domain_strategy"]; got != "ipv6_only" {
		t.Fatalf("socks domain_strategy: got %v", got)
	}

	httpNode := byTag["http-test"]
	if got := httpNode["type"]; got != "http" {
		t.Fatalf("http type: got %v", got)
	}
	httpTLS := mustMapField(t, httpNode, "tls")
	if got := httpTLS["enabled"]; got != true {
		t.Fatalf("http tls.enabled: got %v", got)
	}
	if got := httpTLS["server_name"]; got != "custom.com" {
		t.Fatalf("http tls.server_name: got %v", got)
	}
	if got := httpTLS["insecure"]; got != true {
		t.Fatalf("http tls.insecure: got %v", got)
	}

	wireGuard := byTag["wg-test"]
	if got := wireGuard["type"]; got != "wireguard" {
		t.Fatalf("wireguard type: got %v", got)
	}
	if got := wireGuard["private_key"]; got != "priv-key" {
		t.Fatalf("wireguard private_key: got %v", got)
	}
	if got := wireGuard["peer_public_key"]; got != "pub-key" {
		t.Fatalf("wireguard peer_public_key: got %v", got)
	}
	if got := wireGuard["pre_shared_key"]; got != "psk" {
		t.Fatalf("wireguard pre_shared_key: got %v", got)
	}
	if got := wireGuard["network"]; got != "tcp" {
		t.Fatalf("wireguard network: got %v", got)
	}
	if got := wireGuard["domain_strategy"]; got != "prefer_ipv4" {
		t.Fatalf("wireguard domain_strategy: got %v", got)
	}
	localAddress := mustSliceField(t, wireGuard, "local_address")
	if !containsAnyString(localAddress, "172.16.0.2/32") {
		t.Fatalf("wireguard local_address missing ipv4 entry: %v", localAddress)
	}
	if !containsAnyString(localAddress, "fd01::1/128") {
		t.Fatalf("wireguard local_address missing ipv6 entry: %v", localAddress)
	}
	topReserved := mustSliceField(t, wireGuard, "reserved")
	if len(topReserved) != 3 {
		t.Fatalf("wireguard reserved length: got %d", len(topReserved))
	}

	hysteria := byTag["hy-test"]
	if got := hysteria["type"]; got != "hysteria" {
		t.Fatalf("hysteria type: got %v", got)
	}
	if got := hysteria["up"]; got != "30 Mbps" {
		t.Fatalf("hysteria up: got %v", got)
	}
	if got := hysteria["down"]; got != "200 Mbps" {
		t.Fatalf("hysteria down: got %v", got)
	}
	if got := hysteria["network"]; got != "udp" {
		t.Fatalf("hysteria network: got %v", got)
	}
	serverPorts := mustSliceField(t, hysteria, "server_ports")
	if !containsAnyString(serverPorts, "1000") || !containsAnyString(serverPorts, "2000:3000") {
		t.Fatalf("hysteria server_ports mismatch: %v", serverPorts)
	}

	tuic := byTag["tuic-test"]
	if got := tuic["type"]; got != "tuic" {
		t.Fatalf("tuic type: got %v", got)
	}
	if got := tuic["congestion_control"]; got != "bbr" {
		t.Fatalf("tuic congestion_control: got %v", got)
	}
	if got := tuic["udp_relay_mode"]; got != "native" {
		t.Fatalf("tuic udp_relay_mode: got %v", got)
	}
	if got := tuic["zero_rtt_handshake"]; got != true {
		t.Fatalf("tuic zero_rtt_handshake: got %v", got)
	}
	if got := tuic["heartbeat"]; got != "10000ms" {
		t.Fatalf("tuic heartbeat: got %v", got)
	}
	tuicTLS := mustMapField(t, tuic, "tls")
	if got := tuicTLS["disable_sni"]; got != true {
		t.Fatalf("tuic tls.disable_sni: got %v", got)
	}
	if got := tuicTLS["server_name"]; got != "example.com" {
		t.Fatalf("tuic tls.server_name: got %v", got)
	}

	anytls := byTag["anytls-test"]
	if got := anytls["type"]; got != "anytls" {
		t.Fatalf("anytls type: got %v", got)
	}
	if got := anytls["idle_session_check_interval"]; got != "30s" {
		t.Fatalf("anytls idle_session_check_interval: got %v", got)
	}
	if got := anytls["idle_session_timeout"]; got != "40s" {
		t.Fatalf("anytls idle_session_timeout: got %v", got)
	}
	if got := anytls["min_idle_session"]; got != float64(2) {
		t.Fatalf("anytls min_idle_session: got %v", got)
	}
	anyTLSTLS := mustMapField(t, anytls, "tls")
	utls := mustMapField(t, anyTLSTLS, "utls")
	if got := utls["enabled"]; got != true {
		t.Fatalf("anytls tls.utls.enabled: got %v", got)
	}
	if got := utls["fingerprint"]; got != "chrome" {
		t.Fatalf("anytls tls.utls.fingerprint: got %v", got)
	}

	ssh := byTag["ssh-test"]
	if got := ssh["type"]; got != "ssh" {
		t.Fatalf("ssh type: got %v", got)
	}
	if got := ssh["user"]; got != "root" {
		t.Fatalf("ssh user: got %v", got)
	}
	if got := ssh["private_key"]; got != "key" {
		t.Fatalf("ssh private_key: got %v", got)
	}
	if got := ssh["private_key_passphrase"]; got != "key-password" {
		t.Fatalf("ssh private_key_passphrase: got %v", got)
	}
	hostKeyAlgorithms := mustSliceField(t, ssh, "host_key_algorithms")
	if !containsAnyString(hostKeyAlgorithms, "rsa") {
		t.Fatalf("ssh host_key_algorithms: got %v", hostKeyAlgorithms)
	}
}

func TestParseGeneralSubscription_ClashJSON_V2RayExtendedTransports(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "vmess-grpc",
				"type": "vmess",
				"server": "1.1.1.1",
				"port": 443,
				"uuid": "00000000-0000-0000-0000-000000000011",
				"network": "grpc",
				"tls": true,
				"grpc-opts": {"grpc-service-name": "svc-vmess"}
			},
			{
				"name": "vmess-h2",
				"type": "vmess",
				"server": "1.1.1.2",
				"port": 443,
				"uuid": "00000000-0000-0000-0000-000000000012",
				"network": "h2",
				"h2-opts": {"host": ["h2.example.com"], "path": "/h2"}
			},
			{
				"name": "trojan-grpc",
				"type": "trojan",
				"server": "1.1.1.3",
				"port": 443,
				"password": "pwd",
				"network": "grpc",
				"grpc-opts": {"grpc-service-name": "svc-trojan"}
			},
			{
				"name": "vless-httpupgrade",
				"type": "vless",
				"server": "1.1.1.4",
				"port": 443,
				"uuid": "00000000-0000-0000-0000-000000000014",
				"network": "httpupgrade",
				"http-upgrade-opts": {"host": "upgrade.example.com", "path": "/upgrade"}
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)

	vmessGRPC := byTag["vmess-grpc"]
	vmessGRPCTransport := mustMapField(t, vmessGRPC, "transport")
	if got := vmessGRPCTransport["type"]; got != "grpc" {
		t.Fatalf("vmess-grpc transport.type: got %v", got)
	}
	if got := vmessGRPCTransport["service_name"]; got != "svc-vmess" {
		t.Fatalf("vmess-grpc service_name: got %v", got)
	}

	vmessH2 := byTag["vmess-h2"]
	vmessH2Transport := mustMapField(t, vmessH2, "transport")
	if got := vmessH2Transport["type"]; got != "http" {
		t.Fatalf("vmess-h2 transport.type: got %v", got)
	}
	if got := vmessH2Transport["path"]; got != "/h2" {
		t.Fatalf("vmess-h2 path: got %v", got)
	}
	vmessH2Host := mustSliceField(t, vmessH2Transport, "host")
	if !containsAnyString(vmessH2Host, "h2.example.com") {
		t.Fatalf("vmess-h2 host: got %v", vmessH2Host)
	}

	trojanGRPC := byTag["trojan-grpc"]
	trojanGRPCTransport := mustMapField(t, trojanGRPC, "transport")
	if got := trojanGRPCTransport["type"]; got != "grpc" {
		t.Fatalf("trojan-grpc transport.type: got %v", got)
	}
	if got := trojanGRPCTransport["service_name"]; got != "svc-trojan" {
		t.Fatalf("trojan-grpc service_name: got %v", got)
	}

	vlessHTTPUpgrade := byTag["vless-httpupgrade"]
	vlessHTTPUpgradeTransport := mustMapField(t, vlessHTTPUpgrade, "transport")
	if got := vlessHTTPUpgradeTransport["type"]; got != "httpupgrade" {
		t.Fatalf("vless-httpupgrade transport.type: got %v", got)
	}
	if got := vlessHTTPUpgradeTransport["host"]; got != "upgrade.example.com" {
		t.Fatalf("vless-httpupgrade host: got %v", got)
	}
	if got := vlessHTTPUpgradeTransport["path"]; got != "/upgrade" {
		t.Fatalf("vless-httpupgrade path: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_TUICWithoutUUIDIsSkipped(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "tuic-token-only",
				"type": "tuic",
				"server": "www.example.com",
				"port": 10443,
				"token": "TOKEN"
			},
			{
				"name": "ss-test",
				"type": "ss",
				"server": "1.1.1.1",
				"port": 8388,
				"cipher": "aes-128-gcm",
				"password": "pass"
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
	if got := obj["tag"]; got != "ss-test" {
		t.Fatalf("expected ss-test to remain, got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_WireGuardMissingAddressIsSkipped(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "wg-missing-address",
				"type": "wireguard",
				"server": "162.159.192.1",
				"port": 2480,
				"private-key": "priv-key",
				"public-key": "pub-key"
			},
			{
				"name": "http-ok",
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
	if got := obj["type"]; got != "http" {
		t.Fatalf("expected remaining node type http, got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_WireGuardMissingAllowedIPsUsesDefault(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "wg-missing-allowed-ips",
				"type": "wireguard",
				"server": "162.159.192.1",
				"port": 2480,
				"private-key": "priv-key",
				"public-key": "pub-key",
				"ip": "172.16.0.2"
			},
			{
				"name": "socks-ok",
				"type": "socks5",
				"server": "1.1.1.1",
				"port": 1080
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)
	wireGuard := byTag["wg-missing-allowed-ips"]
	if got := wireGuard["type"]; got != "wireguard" {
		t.Fatalf("wireguard type: got %v", got)
	}
	peers := mustSliceField(t, wireGuard, "peers")
	if len(peers) != 1 {
		t.Fatalf("wireguard peers length: got %d", len(peers))
	}
	firstPeer, ok := peers[0].(map[string]any)
	if !ok {
		t.Fatalf("wireguard peers[0] expected map[string]any, got %T", peers[0])
	}
	allowedIPs := mustSliceField(t, firstPeer, "allowed_ips")
	if !containsAnyString(allowedIPs, "0.0.0.0/0") || !containsAnyString(allowedIPs, "::/0") {
		t.Fatalf("wireguard allowed_ips: got %v", allowedIPs)
	}
}

func TestParseGeneralSubscription_ClashJSON_HysteriaNonUDPProtocolIgnored(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "hy-faketcp",
				"type": "hysteria",
				"server": "server.com",
				"port": 443,
				"auth-str": "yourpassword",
				"up": "30 Mbps",
				"down": "200 Mbps",
				"protocol": "faketcp"
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
	if got := obj["type"]; got != "hysteria" {
		t.Fatalf("expected hysteria node, got %v", got)
	}
	if _, exists := obj["network"]; exists {
		t.Fatalf("expected protocol=faketcp to be ignored, got network=%v", obj["network"])
	}
}

func TestParseGeneralSubscription_ClashJSON_HysteriaAdvancedFields(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "hy-advanced",
				"type": "hysteria",
				"server": "hy.example.com",
				"port": 443,
				"auth-str": "token",
				"up": "30",
				"down": "100",
				"fingerprint": "chrome",
				"ca": "/etc/ssl/certs/custom.pem",
				"ca-str": "-----BEGIN CERTIFICATE-----ABC",
				"hop-interval": 15
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
	if got := obj["type"]; got != "hysteria" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["hop_interval"]; got != "15s" {
		t.Fatalf("hop_interval: got %v", got)
	}
	tls := mustMapField(t, obj, "tls")
	if got := tls["certificate_path"]; got != "/etc/ssl/certs/custom.pem" {
		t.Fatalf("tls.certificate_path: got %v", got)
	}
	certificates := mustSliceField(t, tls, "certificate")
	if !containsAnyString(certificates, "-----BEGIN CERTIFICATE-----ABC") {
		t.Fatalf("tls.certificate: got %v", certificates)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["fingerprint"]; got != "chrome" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_VLESSRealityOpts(t *testing.T) {
	data := []byte(`{
		"proxies": [
				{
					"name": "vless-reality",
					"type": "vless",
					"server": "203.0.113.10",
					"port": 2053,
					"uuid": "11111111-2222-3333-4444-555555555555",
					"tls": true,
					"flow": "xtls-rprx-vision",
					"client-fingerprint": "chrome",
					"network": "tcp",
					"servername": "reality.example.com",
					"reality-opts": {
						"public-key": "test-public-key-abcdef1234567890",
						"short-id": "0123456789abcd"
					}
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
	tls := mustMapField(t, obj, "tls")
	reality := mustMapField(t, tls, "reality")
	if got := reality["enabled"]; got != true {
		t.Fatalf("tls.reality.enabled: got %v", got)
	}
	if got := reality["public_key"]; got != "test-public-key-abcdef1234567890" {
		t.Fatalf("tls.reality.public_key: got %v", got)
	}
	if got := reality["short_id"]; got != "0123456789abcd" {
		t.Fatalf("tls.reality.short_id: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_VLESSFlowNoneIgnored(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "vless-none-flow",
				"type": "vless",
				"server": "203.0.113.20",
				"port": 443,
				"uuid": "11111111-2222-3333-4444-555555555556",
				"tls": true,
				"flow": "None",
				"network": "tcp",
				"servername": "example.com"
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
	if _, exists := obj["flow"]; exists {
		t.Fatalf("flow should be omitted for placeholder value, got %v", obj["flow"])
	}
}

func TestParseGeneralSubscription_ClashJSON_VLESSWSDropsALPN(t *testing.T) {
	data := []byte(`{
		"proxies": [
				{
					"name": "vless-ws-alpn",
					"type": "vless",
					"server": "ws-edge.example.com",
					"port": 443,
					"uuid": "22222222-3333-4444-5555-666666666666",
					"tls": true,
					"client-fingerprint": "chrome",
					"alpn": ["h2", "http/1.1"],
					"network": "ws",
					"ws-opts": {
						"path": "/ws-test-path"
					},
					"servername": "ws-edge.example.com"
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
	tls := mustMapField(t, obj, "tls")
	alpn := mustSliceField(t, tls, "alpn")
	if len(alpn) != 1 || alpn[0] != "http/1.1" {
		t.Fatalf("tls.alpn for ws transport: got %v, want [http/1.1]", alpn)
	}
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_Hysteria2AdvancedFields(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "hy2-advanced",
				"type": "hysteria2",
				"server": "hy2.example.com",
				"port": 443,
				"password": "password",
				"ports": "443,8443",
				"up": 20,
				"down": "60",
				"obfs": "salamander",
				"obfs-password": "obfs-secret",
				"hop-interval": 12,
				"fingerprint": "firefox",
				"ca": "/etc/ssl/certs/hy2.pem",
				"ca-str": "-----BEGIN CERTIFICATE-----XYZ"
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
	if got := obj["type"]; got != "hysteria2" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["up_mbps"]; got != float64(20) {
		t.Fatalf("up_mbps: got %v", got)
	}
	if got := obj["down_mbps"]; got != float64(60) {
		t.Fatalf("down_mbps: got %v", got)
	}
	if got := obj["hop_interval"]; got != "12s" {
		t.Fatalf("hop_interval: got %v", got)
	}
	serverPorts := mustSliceField(t, obj, "server_ports")
	if !containsAnyString(serverPorts, "443") || !containsAnyString(serverPorts, "8443") {
		t.Fatalf("server_ports: got %v", serverPorts)
	}
	obfs := mustMapField(t, obj, "obfs")
	if got := obfs["type"]; got != "salamander" {
		t.Fatalf("obfs.type: got %v", got)
	}
	if got := obfs["password"]; got != "obfs-secret" {
		t.Fatalf("obfs.password: got %v", got)
	}
	tls := mustMapField(t, obj, "tls")
	if got := tls["certificate_path"]; got != "/etc/ssl/certs/hy2.pem" {
		t.Fatalf("tls.certificate_path: got %v", got)
	}
	certificates := mustSliceField(t, tls, "certificate")
	if !containsAnyString(certificates, "-----BEGIN CERTIFICATE-----XYZ") {
		t.Fatalf("tls.certificate: got %v", certificates)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["fingerprint"]; got != "firefox" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_HTTPAndSOCKSUnsupportedFieldsIgnored(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "socks-extra",
				"type": "socks",
				"server": "1.1.1.1",
				"port": 1080,
				"tls": true,
				"fingerprint": "xxxx",
				"skip-cert-verify": true
			},
			{
				"name": "http-extra",
				"type": "http",
				"server": "2.2.2.2",
				"port": 443,
				"tls": true,
				"sni": "custom.com",
				"fingerprint": "xxxx"
			}
		]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)
	socks := byTag["socks-extra"]
	if _, exists := socks["tls"]; exists {
		t.Fatalf("expected socks tls to be ignored, got %v", socks["tls"])
	}
	httpNode := byTag["http-extra"]
	if _, exists := httpNode["fingerprint"]; exists {
		t.Fatalf("expected http fingerprint to be ignored, got %v", httpNode["fingerprint"])
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

func TestParseGeneralSubscription_VLESSURIFlowNoneIgnored(t *testing.T) {
	data := []byte(
		"vless://11111111-2222-3333-4444-555555555557@example.com:443?type=tcp&security=tls&sni=example.com&flow=None",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if _, exists := obj["flow"]; exists {
		t.Fatalf("flow should be omitted for placeholder value, got %v", obj["flow"])
	}
}

func TestParseGeneralSubscription_VMess1URILine(t *testing.T) {
	data := []byte(
		"vmess1://11111111-2222-3333-4444-555555555555@example.com:443?network=ws&tls=true&ws.host=ws.example.com&path=%2Fws#VMESS1%20Node",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "vmess" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["tag"]; got != "VMESS1 Node" {
		t.Fatalf("tag: got %v", got)
	}
	if got := obj["uuid"]; got != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("uuid: got %v", got)
	}
	tls := mustMapField(t, obj, "tls")
	if got := tls["enabled"]; got != true {
		t.Fatalf("tls.enabled: got %v", got)
	}
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/ws" {
		t.Fatalf("transport.path: got %v", got)
	}
	headers := mustMapField(t, transport, "headers")
	if got := headers["Host"]; got != "ws.example.com" {
		t.Fatalf("transport.headers.Host: got %v", got)
	}
}

func TestParseGeneralSubscription_SSDURI(t *testing.T) {
	ssd := `{
		"airport":"SSD-Airport",
		"port":8388,
		"encryption":"aes-128-gcm",
		"password":"default-pass",
		"plugin":"v2ray-plugin",
		"plugin_options":"mode=websocket;host=ws.example.com",
		"servers":[
			{"server":"1.1.1.1","remarks":"ssd-a"},
			{"server":"2.2.2.2","port":9443,"encryption":"chacha20-ietf-poly1305","password":"node-pass"}
		]
	}`
	data := []byte("ssd://" + base64.StdEncoding.EncodeToString([]byte(ssd)))

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)
	a := byTag["ssd-a"]
	if got := a["type"]; got != "shadowsocks" {
		t.Fatalf("ssd-a type: got %v", got)
	}
	if got := a["plugin"]; got != "v2ray-plugin" {
		t.Fatalf("ssd-a plugin: got %v", got)
	}
	if got := a["plugin_opts"]; got != "mode=websocket;host=ws.example.com" {
		t.Fatalf("ssd-a plugin_opts: got %v", got)
	}

	var second map[string]any
	for _, node := range nodes {
		obj := parseNodeRaw(t, node.RawOptions)
		if obj["tag"] != "ssd-a" {
			second = obj
			break
		}
	}
	if second == nil {
		t.Fatal("second SSD node not found")
	}
	if got := second["server"]; got != "2.2.2.2" {
		t.Fatalf("ssd second server: got %v", got)
	}
	if got := second["server_port"]; got != float64(9443) {
		t.Fatalf("ssd second server_port: got %v", got)
	}
	if got := second["password"]; got != "node-pass" {
		t.Fatalf("ssd second password: got %v", got)
	}
}

func TestParseGeneralSubscription_SSDURIWithSimpleObfsAlias(t *testing.T) {
	ssd := `{
		"airport":"SSD-Airport",
		"port":8388,
		"encryption":"aes-128-gcm",
		"password":"default-pass",
		"plugin":"simple-obfs",
		"plugin_options":"mode=http;host=obfs.example.com",
		"servers":[
			{"server":"1.1.1.1","remarks":"ssd-obfs"}
		]
	}`
	data := []byte("ssd://" + base64.StdEncoding.EncodeToString([]byte(ssd)))

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SurgeProxySection(t *testing.T) {
	data := []byte(`
[General]
loglevel = warning

[Proxy]
ss-node = ss, 1.1.1.1, 8388, encrypt-method=aes-128-gcm, password=pass, obfs=http, obfs-host=obfs.example.com
vmess-node = vmess, 2.2.2.2, 443, username=11111111-2222-3333-4444-555555555556, ws=true, ws-path=/ws, ws-headers=Host:ws.example.com, tls=true, skip-cert-verify=true
trojan-node = trojan, 3.3.3.3, 443, password=trojan-pass, sni=trojan.example.com
socks-node = socks5, 4.4.4.4, 1080, username=socks-user, password=socks-pass
http-node = https, 5.5.5.5, 8443, username=http-user, password=http-pass, sni=http.example.com, skip-cert-verify=true
snell-node = snell, 6.6.6.6, 443, psk=abc

[Rule]
FINAL,DIRECT
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 {
		t.Fatalf("expected 5 parsed nodes, got %d", len(nodes))
	}

	byTag := parseNodesByTag(t, nodes)

	ss := byTag["ss-node"]
	if got := ss["type"]; got != "shadowsocks" {
		t.Fatalf("ss-node type: got %v", got)
	}
	if got := ss["plugin"]; got != "obfs-local" {
		t.Fatalf("ss-node plugin: got %v", got)
	}
	if got := ss["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("ss-node plugin_opts: got %v", got)
	}

	vmess := byTag["vmess-node"]
	if got := vmess["type"]; got != "vmess" {
		t.Fatalf("vmess-node type: got %v", got)
	}
	vmessTLS := mustMapField(t, vmess, "tls")
	if got := vmessTLS["insecure"]; got != true {
		t.Fatalf("vmess-node tls.insecure: got %v", got)
	}
	vmessTransport := mustMapField(t, vmess, "transport")
	if got := vmessTransport["type"]; got != "ws" {
		t.Fatalf("vmess-node transport.type: got %v", got)
	}

	trojan := byTag["trojan-node"]
	if got := trojan["type"]; got != "trojan" {
		t.Fatalf("trojan-node type: got %v", got)
	}
	trojanTLS := mustMapField(t, trojan, "tls")
	if got := trojanTLS["server_name"]; got != "trojan.example.com" {
		t.Fatalf("trojan-node tls.server_name: got %v", got)
	}

	socks := byTag["socks-node"]
	if got := socks["type"]; got != "socks" {
		t.Fatalf("socks-node type: got %v", got)
	}
	if got := socks["username"]; got != "socks-user" {
		t.Fatalf("socks-node username: got %v", got)
	}

	httpNode := byTag["http-node"]
	if got := httpNode["type"]; got != "http" {
		t.Fatalf("http-node type: got %v", got)
	}
	httpTLS := mustMapField(t, httpNode, "tls")
	if got := httpTLS["enabled"]; got != true {
		t.Fatalf("http-node tls.enabled: got %v", got)
	}
	if got := httpTLS["server_name"]; got != "http.example.com" {
		t.Fatalf("http-node tls.server_name: got %v", got)
	}
	if got := httpTLS["insecure"]; got != true {
		t.Fatalf("http-node tls.insecure: got %v", got)
	}
}

func TestParseGeneralSubscription_SurgeProxySection_TooLongLineReturnsError(t *testing.T) {
	tooLong := strings.Repeat("a", surgeScannerMaxTokenSize+32)
	data := []byte(
		"[Proxy]\n" +
			"vmess-node = vmess, 2.2.2.2, 443, username=11111111-2222-3333-4444-555555555556, ws=true, ws-headers=Host:" + tooLong + "\n",
	)

	_, err := ParseGeneralSubscription(data)
	if err == nil {
		t.Fatal("expected error for surge line exceeding scanner token limit")
	}
	if !strings.Contains(err.Error(), "scan surge proxy") {
		t.Fatalf("expected scan surge proxy error, got: %v", err)
	}
}

func TestParseGeneralSubscription_VLESSWSPathWithEarlyDataQuery(t *testing.T) {
	data := []byte(
		"vless://11111111-2222-3333-4444-555555555555@edge.example.net:443?encryption=none&security=tls&sni=ws-edge.example.net&type=ws&host=ws-edge.example.net&path=%2Fvless-argo%3Fed%3D2560",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/vless-argo" {
		t.Fatalf("transport.path: got %v, want /vless-argo", got)
	}
	if got := transport["max_early_data"]; got != float64(2560) {
		t.Fatalf("transport.max_early_data: got %v, want 2560", got)
	}
	if got := transport["early_data_header_name"]; got != "Sec-WebSocket-Protocol" {
		t.Fatalf("transport.early_data_header_name: got %v", got)
	}
	headers := mustMapField(t, transport, "headers")
	if got := headers["Host"]; got != "ws-edge.example.net" {
		t.Fatalf("transport.headers.Host: got %v", got)
	}
}

func TestParseGeneralSubscription_VLESSWSPathUnknownQueryPreserved(t *testing.T) {
	data := []byte(
		"vless://26a1d547-b031-4139-9fc5-6671e1d0408a@example.com:443?type=ws&security=tls&sni=example.com&path=%2Fvless-argo%3Ffoo%3Dbar",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	transport := mustMapField(t, obj, "transport")
	if got := transport["path"]; got != "/vless-argo?foo=bar" {
		t.Fatalf("transport.path: got %v, want /vless-argo?foo=bar", got)
	}
	if _, ok := transport["max_early_data"]; ok {
		t.Fatalf("transport.max_early_data should be absent, got %v", transport["max_early_data"])
	}
	if _, ok := transport["early_data_header_name"]; ok {
		t.Fatalf("transport.early_data_header_name should be absent, got %v", transport["early_data_header_name"])
	}
}

func TestParseGeneralSubscription_VLESSURIWSDropsALPN(t *testing.T) {
	data := []byte(
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=ws&security=tls&sni=example.com&alpn=h2%2Ch3&path=%2Fws",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	if _, ok := tls["alpn"]; ok {
		t.Fatalf("tls.alpn should be absent for ws transport, got %v", tls["alpn"])
	}
}

func TestParseGeneralSubscription_VLESSURITLSAdvancedFields(t *testing.T) {
	data := []byte(
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp&security=tls&sni=example.com&allowInsecure=1&alpn=h2%2Ch3&fp=firefox",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	if got := tls["enabled"]; got != true {
		t.Fatalf("tls.enabled: got %v", got)
	}
	if got := tls["insecure"]; got != true {
		t.Fatalf("tls.insecure: got %v", got)
	}
	alpn, ok := tls["alpn"].([]any)
	if !ok {
		t.Fatalf("tls.alpn expected []any, got %T", tls["alpn"])
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "h3" {
		t.Fatalf("tls.alpn: got %v, want [h2 h3]", alpn)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["enabled"]; got != true {
		t.Fatalf("tls.utls.enabled: got %v", got)
	}
	if got := utls["fingerprint"]; got != "firefox" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_VLESSURIRealityFields(t *testing.T) {
	data := []byte(
		"vless://11111111-2222-3333-4444-555555555555@example.com:443?type=tcp&security=reality&sni=example.com&pbk=R1f59A5fR4m6SZHjH2lSQw4mYcpq2bHKuX1N0rD2wQ0&sid=11aa",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	reality := mustMapField(t, tls, "reality")
	if got := reality["enabled"]; got != true {
		t.Fatalf("tls.reality.enabled: got %v", got)
	}
	if got := reality["public_key"]; got != "R1f59A5fR4m6SZHjH2lSQw4mYcpq2bHKuX1N0rD2wQ0" {
		t.Fatalf("tls.reality.public_key: got %v", got)
	}
	if got := reality["short_id"]; got != "11aa" {
		t.Fatalf("tls.reality.short_id: got %v", got)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["enabled"]; got != true {
		t.Fatalf("tls.utls.enabled: got %v", got)
	}
	if got := utls["fingerprint"]; got != "chrome" {
		t.Fatalf("tls.utls.fingerprint: got %v, want chrome default", got)
	}
}

func TestParseGeneralSubscription_VLESSURIExtendedTransports(t *testing.T) {
	data := []byte(`
vless://11111111-2222-3333-4444-555555555551@example.com:443?type=grpc&security=tls&serviceName=vless-grpc
vless://11111111-2222-3333-4444-555555555552@example.com:443?type=h2&security=tls&host=h2.example.com,h2b.example.com&path=%2Fh2
vless://11111111-2222-3333-4444-555555555553@example.com:443?type=httpupgrade&security=tls&host=up.example.com&path=%2Fupgrade
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 parsed nodes, got %d", len(nodes))
	}

	grpcNode := parseNodeRaw(t, nodes[0].RawOptions)
	grpcTransport := mustMapField(t, grpcNode, "transport")
	if got := grpcTransport["type"]; got != "grpc" {
		t.Fatalf("grpc transport.type: got %v", got)
	}
	if got := grpcTransport["service_name"]; got != "vless-grpc" {
		t.Fatalf("grpc service_name: got %v", got)
	}

	h2Node := parseNodeRaw(t, nodes[1].RawOptions)
	h2Transport := mustMapField(t, h2Node, "transport")
	if got := h2Transport["type"]; got != "http" {
		t.Fatalf("h2 transport.type: got %v", got)
	}
	if got := h2Transport["path"]; got != "/h2" {
		t.Fatalf("h2 transport.path: got %v", got)
	}
	h2Hosts := mustSliceField(t, h2Transport, "host")
	if !containsAnyString(h2Hosts, "h2.example.com") || !containsAnyString(h2Hosts, "h2b.example.com") {
		t.Fatalf("h2 transport.host: got %v", h2Hosts)
	}

	httpUpgradeNode := parseNodeRaw(t, nodes[2].RawOptions)
	httpUpgradeTransport := mustMapField(t, httpUpgradeNode, "transport")
	if got := httpUpgradeTransport["type"]; got != "httpupgrade" {
		t.Fatalf("httpupgrade transport.type: got %v", got)
	}
	if got := httpUpgradeTransport["host"]; got != "up.example.com" {
		t.Fatalf("httpupgrade transport.host: got %v", got)
	}
	if got := httpUpgradeTransport["path"]; got != "/upgrade" {
		t.Fatalf("httpupgrade transport.path: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURITLSAdvancedFields(t *testing.T) {
	vmessPayload := `{"v":"2","ps":"vmess-test","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","net":"tcp","type":"none","tls":"tls","allowInsecure":"1","alpn":"h2,h3","fp":"safari"}`
	data := []byte("vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessPayload)))

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	if got := tls["insecure"]; got != true {
		t.Fatalf("tls.insecure: got %v", got)
	}
	alpn, ok := tls["alpn"].([]any)
	if !ok {
		t.Fatalf("tls.alpn expected []any, got %T", tls["alpn"])
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "h3" {
		t.Fatalf("tls.alpn: got %v, want [h2 h3]", alpn)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["fingerprint"]; got != "safari" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURIExtendedTransports(t *testing.T) {
	vmessGRPC := `{"v":"2","ps":"vmess-grpc","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555556","aid":"0","net":"grpc","path":"svc-vmess-grpc"}`
	vmessH2 := `{"v":"2","ps":"vmess-h2","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555557","aid":"0","net":"h2","host":"h2.example.com,h2b.example.com","path":"/h2"}`

	data := []byte(
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessGRPC)) + "\n" +
			"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessH2)),
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}

	grpcNode := parseNodeRaw(t, nodes[0].RawOptions)
	grpcTransport := mustMapField(t, grpcNode, "transport")
	if got := grpcTransport["type"]; got != "grpc" {
		t.Fatalf("grpc transport.type: got %v", got)
	}
	if got := grpcTransport["service_name"]; got != "svc-vmess-grpc" {
		t.Fatalf("grpc service_name: got %v", got)
	}

	h2Node := parseNodeRaw(t, nodes[1].RawOptions)
	h2Transport := mustMapField(t, h2Node, "transport")
	if got := h2Transport["type"]; got != "http" {
		t.Fatalf("h2 transport.type: got %v", got)
	}
	if got := h2Transport["path"]; got != "/h2" {
		t.Fatalf("h2 transport.path: got %v", got)
	}
	h2Hosts := mustSliceField(t, h2Transport, "host")
	if !containsAnyString(h2Hosts, "h2.example.com") || !containsAnyString(h2Hosts, "h2b.example.com") {
		t.Fatalf("h2 transport.host: got %v", h2Hosts)
	}
}

func TestParseGeneralSubscription_TrojanURITLSAdvancedFields(t *testing.T) {
	data := []byte(
		"trojan://password@example.com:443?type=tcp&sni=example.com&allowInsecure=1&alpn=h2%2Chttp%2F1.1&fp=edge",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	if got := tls["insecure"]; got != true {
		t.Fatalf("tls.insecure: got %v", got)
	}
	alpn, ok := tls["alpn"].([]any)
	if !ok {
		t.Fatalf("tls.alpn expected []any, got %T", tls["alpn"])
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("tls.alpn: got %v, want [h2 http/1.1]", alpn)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["fingerprint"]; got != "edge" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_TrojanURIExtendedTransports(t *testing.T) {
	data := []byte(`
trojan://password@example.com:443?type=grpc&serviceName=trojan-grpc
trojan://password@example.com:443?type=h2&host=h2.example.com&path=%2Fh2
trojan://password@example.com:443?type=httpupgrade&host=upgrade.example.com&path=%2Fupgrade
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 parsed nodes, got %d", len(nodes))
	}

	grpcNode := parseNodeRaw(t, nodes[0].RawOptions)
	grpcTransport := mustMapField(t, grpcNode, "transport")
	if got := grpcTransport["type"]; got != "grpc" {
		t.Fatalf("grpc transport.type: got %v", got)
	}
	if got := grpcTransport["service_name"]; got != "trojan-grpc" {
		t.Fatalf("grpc service_name: got %v", got)
	}

	h2Node := parseNodeRaw(t, nodes[1].RawOptions)
	h2Transport := mustMapField(t, h2Node, "transport")
	if got := h2Transport["type"]; got != "http" {
		t.Fatalf("h2 transport.type: got %v", got)
	}
	if got := h2Transport["path"]; got != "/h2" {
		t.Fatalf("h2 transport.path: got %v", got)
	}

	httpUpgradeNode := parseNodeRaw(t, nodes[2].RawOptions)
	httpUpgradeTransport := mustMapField(t, httpUpgradeNode, "transport")
	if got := httpUpgradeTransport["type"]; got != "httpupgrade" {
		t.Fatalf("httpupgrade transport.type: got %v", got)
	}
	if got := httpUpgradeTransport["host"]; got != "upgrade.example.com" {
		t.Fatalf("httpupgrade transport.host: got %v", got)
	}
	if got := httpUpgradeTransport["path"]; got != "/upgrade" {
		t.Fatalf("httpupgrade transport.path: got %v", got)
	}
}

func TestParseGeneralSubscription_HY2URIAliasAndQueryPassword(t *testing.T) {
	data := []byte(
		"hy2://hy2.example.com:443?password=hy2-password&sni=hy2.example.com&obfs=salamander&obfs-password=obfs-secret&ports=443,8443&up=20&down=80&hop-interval=10&fp=chrome&ca=%2Fetc%2Fssl%2Fcerts%2Fhy2.pem&ca-str=-----BEGIN%20CERT-----",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "hysteria2" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["password"]; got != "hy2-password" {
		t.Fatalf("password: got %v", got)
	}
	if got := obj["up_mbps"]; got != float64(20) {
		t.Fatalf("up_mbps: got %v", got)
	}
	if got := obj["down_mbps"]; got != float64(80) {
		t.Fatalf("down_mbps: got %v", got)
	}
	if got := obj["hop_interval"]; got != "10s" {
		t.Fatalf("hop_interval: got %v", got)
	}
	obfs := mustMapField(t, obj, "obfs")
	if got := obfs["type"]; got != "salamander" {
		t.Fatalf("obfs.type: got %v", got)
	}
	if got := obfs["password"]; got != "obfs-secret" {
		t.Fatalf("obfs.password: got %v", got)
	}
	tls := mustMapField(t, obj, "tls")
	if got := tls["certificate_path"]; got != "/etc/ssl/certs/hy2.pem" {
		t.Fatalf("tls.certificate_path: got %v", got)
	}
	utls := mustMapField(t, tls, "utls")
	if got := utls["fingerprint"]; got != "chrome" {
		t.Fatalf("tls.utls.fingerprint: got %v", got)
	}
}

func TestParseGeneralSubscription_HY2URIMPortRangeNormalizedToSingBoxFormat(t *testing.T) {
	data := []byte("hy2://hy2-password@hy2.example.com:20000?sni=hy2.example.com&mport=20000-50000")

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	serverPorts := mustSliceField(t, obj, "server_ports")
	if !containsAnyString(serverPorts, "20000:50000") {
		t.Fatalf("server_ports: got %v", serverPorts)
	}
}

func TestParseGeneralSubscription_HY2URIUserPassAuth(t *testing.T) {
	data := []byte("hy2://hy2-user:hy2-pass@hy2.example.com:443?sni=hy2.example.com")

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["password"]; got != "hy2-user:hy2-pass" {
		t.Fatalf("password: got %v", got)
	}
}

func TestParseGeneralSubscription_HY2URIPinSHA256MapsToTLSCertPublicKeyPin(t *testing.T) {
	data := []byte("hy2://hy2-password@hy2.example.com:443?pinSHA256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=&sni=hy2.example.com")

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	tls := mustMapField(t, obj, "tls")
	pins := mustSliceField(t, tls, "certificate_public_key_sha256")
	if !containsAnyString(pins, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") {
		t.Fatalf("tls.certificate_public_key_sha256: got %v", pins)
	}
	if _, hasUTLS := tls["utls"]; hasUTLS {
		t.Fatalf("tls.utls should be absent when only pinSHA256 is provided: got %v", tls["utls"])
	}
}

func TestParseGeneralSubscription_SSURIWithPluginOptions(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Dws.example.com%3Btls#ss-plugin",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "v2ray-plugin" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "mode=websocket;host=ws.example.com;tls" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIWithSeparatedPluginAndPluginOpts(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=v2ray-plugin&plugin-opts=mode%3Dwebsocket%3Bhost%3Dws.example.com%3Btls#ss-plugin-separated",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "v2ray-plugin" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "mode=websocket;host=ws.example.com;tls" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIWithPluginOptionsUnescapedSemicolons(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=v2ray-plugin;mode=websocket;host=ws.example.com;tls#ss-plugin-raw",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "v2ray-plugin" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "mode=websocket;host=ws.example.com;tls" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIWithSimpleObfsAlias(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=simple-obfs%3Bobfs%3Dhttp%3Bobfs-host%3Dobfs.example.com#ss-simple-obfs",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIWithSeparatedSimpleObfsAliasOptions(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=simple-obfs&plugin-opts=obfs-local%3Bmode%3Dhttp%3Bhost%3Dobfs.example.com#ss-simple-obfs-separated",
	)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIWithSimpleObfsEscapedOptions(t *testing.T) {
	plugin := url.QueryEscape(`simple-obfs;obfs=http;obfs-host=edge\;host.example.com;path=/a\=b`)
	data := []byte("ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388?plugin=" + plugin + "#ss-simple-obfs-escaped")

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != `obfs=http;obfs-host=edge\;host.example.com;path=/a\=b` {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashSSWithObfsPluginAlias(t *testing.T) {
	data := []byte(`{
		"proxies": [{
			"name": "tag-lax",
			"type": "ss",
			"server": "1.1.1.1",
			"port": 8388,
			"cipher": "aes-128-gcm",
			"password": "pass",
			"plugin": "obfs",
			"plugin-opts": {
				"mode": "http",
				"host": "obfs.example.com"
			}
		}]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashSSWithObfsLocalModeHostOptions(t *testing.T) {
	data := []byte(`{
		"proxies": [{
			"name": "tag-lax",
			"type": "ss",
			"server": "1.1.1.1",
			"port": 8388,
			"cipher": "aes-128-gcm",
			"password": "pass",
			"plugin": "obfs-local",
			"plugin-opts": {
				"mode": "http",
				"host": "obfs.example.com"
			}
		}]
	}`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_SSURIDefaultTagUsesEndpoint(t *testing.T) {
	nodes, err := ParseGeneralSubscription([]byte("ss://YWVzLTEyOC1nY206cGFzcw==@1.1.1.1:8388"))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["tag"]; got != "shadowsocks-1.1.1.1:8388" {
		t.Fatalf("tag: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURIStandardFormat(t *testing.T) {
	data := []byte("vmess://ws+tls:11111111-2222-3333-4444-555555555555-0@example.com:443?host=ws.example.com&path=%2Fws#std-vmess")
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "vmess" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["tag"]; got != "std-vmess" {
		t.Fatalf("tag: got %v", got)
	}
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/ws" {
		t.Fatalf("transport.path: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURIJSONTypeNoneWithoutNetDefaultsToTCP(t *testing.T) {
	vmessPayload := `{"v":"2","ps":"vmess-type-none","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555558","aid":"0","type":"none","tls":"tls"}`
	data := []byte("vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessPayload)))

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "vmess" {
		t.Fatalf("type: got %v", got)
	}
	if _, hasTransport := obj["transport"]; hasTransport {
		t.Fatalf("transport should be absent for tcp default, got %v", obj["transport"])
	}
}

func TestParseGeneralSubscription_VMessURIShadowrocketFormat(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("auto:11111111-2222-3333-4444-555555555555@example.com:443"))
	data := []byte("vmess://" + secret + "?remarks=sr-node&obfs=websocket&obfsParam=ws.example.com&path=%2Fws&tls=1&allowInsecure=1")
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["tag"]; got != "sr-node" {
		t.Fatalf("tag: got %v", got)
	}
	tls := mustMapField(t, obj, "tls")
	if got := tls["insecure"]; got != true {
		t.Fatalf("tls.insecure: got %v", got)
	}
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURIQuanPayload(t *testing.T) {
	plain := `quan-node = vmess,example.com,443,auto,11111111-2222-3333-4444-555555555555,obfs=ws,obfs-path="/ws",obfs-header="Host: ws.example.com",over-tls=true`
	data := []byte("vmess://" + base64.StdEncoding.EncodeToString([]byte(plain)))
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["tag"]; got != "quan-node" {
		t.Fatalf("tag: got %v", got)
	}
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
}

func TestParseGeneralSubscription_TrojanURILegacyWSParams(t *testing.T) {
	data := []byte("trojan://password@example.com:443?ws=1&wspath=%2Foldws&sni=example.com")
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/oldws" {
		t.Fatalf("transport.path: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashYAMLLegacyProxyKey(t *testing.T) {
	data := []byte(`
Proxy:
  - name: vmess-legacy-yaml
    type: vmess
    server: 3.3.3.3
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["tag"]; got != "vmess-legacy-yaml" {
		t.Fatalf("tag: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashYAMLLowercaseProxyKey(t *testing.T) {
	data := []byte(`
proxy:
  - name: vmess-lower-yaml
    type: vmess
    server: 4.4.4.4
    port: 443
    uuid: 11111111-2222-3333-4444-555555555557
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["tag"]; got != "vmess-lower-yaml" {
		t.Fatalf("tag: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_DialFieldsAppliedToVMessAndHY2(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "vmess-dial",
				"type": "vmess",
				"server": "1.1.1.1",
				"port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555",
				"dialer-proxy": "chain-a",
				"bind-interface": "eth1",
				"routing-mark": 30
			},
			{
				"name": "hy2-dial",
				"type": "hysteria2",
				"server": "hy2.example.com",
				"port": 443,
				"password": "hy2-pass",
				"sni": "hy2.example.com",
				"dialer-proxy": "chain-b",
				"bind-interface": "eth2",
				"routing-mark": "0x10"
			}
		]
	}`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}
	byTag := parseNodesByTag(t, nodes)

	vmess := byTag["vmess-dial"]
	if got := vmess["detour"]; got != "chain-a" {
		t.Fatalf("vmess detour: got %v", got)
	}
	if got := vmess["bind_interface"]; got != "eth1" {
		t.Fatalf("vmess bind_interface: got %v", got)
	}
	if got := vmess["routing_mark"]; got != float64(30) {
		t.Fatalf("vmess routing_mark: got %v", got)
	}

	hy2 := byTag["hy2-dial"]
	if got := hy2["detour"]; got != "chain-b" {
		t.Fatalf("hy2 detour: got %v", got)
	}
	if got := hy2["bind_interface"]; got != "eth2" {
		t.Fatalf("hy2 bind_interface: got %v", got)
	}
	if got := hy2["routing_mark"]; got != "0x10" {
		t.Fatalf("hy2 routing_mark: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_HysteriaAllowsMissingRatesAndReadsSpeedFields(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "hy-no-rate",
				"type": "hysteria",
				"server": "hy-no-rate.example.com",
				"port": 443,
				"auth-str": "token",
				"sni": "hy-no-rate.example.com"
			},
			{
				"name": "hy-speed",
				"type": "hysteria",
				"server": "hy-speed.example.com",
				"port": 443,
				"auth-str": "token2",
				"up-speed": 12,
				"down-speed": 34,
				"sni": "hy-speed.example.com"
			}
		]
	}`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 parsed nodes, got %d", len(nodes))
	}
	byTag := parseNodesByTag(t, nodes)

	hyNoRate := byTag["hy-no-rate"]
	if _, ok := hyNoRate["up"]; ok {
		t.Fatalf("hy-no-rate up should be absent, got %v", hyNoRate["up"])
	}
	if _, ok := hyNoRate["down"]; ok {
		t.Fatalf("hy-no-rate down should be absent, got %v", hyNoRate["down"])
	}

	hySpeed := byTag["hy-speed"]
	if got := hySpeed["up"]; got != "12 Mbps" {
		t.Fatalf("hy-speed up: got %v", got)
	}
	if got := hySpeed["down"]; got != "34 Mbps" {
		t.Fatalf("hy-speed down: got %v", got)
	}
}

func TestParseGeneralSubscription_SurgeProxySection_ExtendedProtocols(t *testing.T) {
	data := []byte(`
[Proxy]
vless-node = vless, 1.1.1.1, 443, username=11111111-2222-3333-4444-555555555555, tls=true, sni=vless.example.com
wg-node = wireguard, 162.159.192.1, 2480, private-key=priv-key, public-key=pub-key, ip=172.16.0.2/32, ipv6=fd01::1/128, allowed-ips="0.0.0.0/0,::/0"
hy2-node = hysteria2, hy2.example.com, 443, password=hy2-pass, sni=hy2.example.com, up=20, down=80
tuic-node = tuic, tuic.example.com, 443, uuid=11111111-2222-3333-4444-555555555556, password=tuic-pass, sni=tuic.example.com
ssh-node = ssh, 127.0.0.1, 22, user=root, password=secret
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 {
		t.Fatalf("expected 5 parsed nodes, got %d", len(nodes))
	}
	byTag := parseNodesByTag(t, nodes)

	if got := byTag["vless-node"]["type"]; got != "vless" {
		t.Fatalf("vless-node type: got %v", got)
	}
	if got := byTag["wg-node"]["type"]; got != "wireguard" {
		t.Fatalf("wg-node type: got %v", got)
	}
	if got := byTag["hy2-node"]["type"]; got != "hysteria2" {
		t.Fatalf("hy2-node type: got %v", got)
	}
	if got := byTag["tuic-node"]["type"]; got != "tuic" {
		t.Fatalf("tuic-node type: got %v", got)
	}
	if got := byTag["ssh-node"]["type"]; got != "ssh" {
		t.Fatalf("ssh-node type: got %v", got)
	}
}

func TestParseGeneralSubscription_ProxyURILines(t *testing.T) {
	data := []byte(`
http://user-http:pass-http@1.2.3.4:8080#HTTP%20Node
https://user-https:pass-https@example.com:8443?sni=tls.example.com&allowInsecure=1#HTTPS%20Node
socks5://user-s5:pass-s5@5.6.7.8:1081#SOCKS5%20Node
socks5h://user-s5h:pass-s5h@proxy.example.net:1082#SOCKS5H%20Node
`)

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 parsed nodes, got %d", len(nodes))
	}

	first := parseNodeRaw(t, nodes[0].RawOptions)
	second := parseNodeRaw(t, nodes[1].RawOptions)
	third := parseNodeRaw(t, nodes[2].RawOptions)
	fourth := parseNodeRaw(t, nodes[3].RawOptions)

	if got := first["type"]; got != "http" {
		t.Fatalf("expected first type http, got %v", got)
	}
	if got := first["username"]; got != "user-http" {
		t.Fatalf("expected first username user-http, got %v", got)
	}
	if got := first["password"]; got != "pass-http" {
		t.Fatalf("expected first password pass-http, got %v", got)
	}
	if got := first["tag"]; got != "HTTP Node" {
		t.Fatalf("expected first tag HTTP Node, got %v", got)
	}

	if got := second["type"]; got != "http" {
		t.Fatalf("expected second type http, got %v", got)
	}
	tls, ok := second["tls"].(map[string]any)
	if !ok {
		t.Fatalf("expected second tls object, got %T", second["tls"])
	}
	if got := tls["enabled"]; got != true {
		t.Fatalf("expected second tls.enabled true, got %v", got)
	}
	if got := tls["server_name"]; got != "tls.example.com" {
		t.Fatalf("expected second tls.server_name tls.example.com, got %v", got)
	}
	if got := tls["insecure"]; got != true {
		t.Fatalf("expected second tls.insecure true, got %v", got)
	}
	if got := second["tag"]; got != "HTTPS Node" {
		t.Fatalf("expected second tag HTTPS Node, got %v", got)
	}

	if got := third["type"]; got != "socks" {
		t.Fatalf("expected third type socks, got %v", got)
	}
	if got := third["server"]; got != "5.6.7.8" {
		t.Fatalf("expected third server 5.6.7.8, got %v", got)
	}
	if got := third["server_port"]; got != float64(1081) {
		t.Fatalf("expected third server_port 1081, got %v", got)
	}
	if got := third["username"]; got != "user-s5" {
		t.Fatalf("expected third username user-s5, got %v", got)
	}
	if got := third["password"]; got != "pass-s5" {
		t.Fatalf("expected third password pass-s5, got %v", got)
	}

	if got := fourth["type"]; got != "socks" {
		t.Fatalf("expected fourth type socks, got %v", got)
	}
	if got := fourth["server"]; got != "proxy.example.net" {
		t.Fatalf("expected fourth server proxy.example.net, got %v", got)
	}
	if got := fourth["server_port"]; got != float64(1082) {
		t.Fatalf("expected fourth server_port 1082, got %v", got)
	}
	if got := fourth["username"]; got != "user-s5h" {
		t.Fatalf("expected fourth username user-s5h, got %v", got)
	}
	if got := fourth["password"]; got != "pass-s5h" {
		t.Fatalf("expected fourth password pass-s5h, got %v", got)
	}
}

func TestParseGeneralSubscription_ProxyURILinesRejectNonProxyURLs(t *testing.T) {
	tests := []string{
		"https://api.example.com",
		"https://api.example.com/subscription/token",
		"http://api.example.com:8080/path/to/resource",
		"socks5://proxy.example.com:1080/path",
		"socks5://proxy.example.com:1080?token=abc",
		"https://proxy.example.com:8443?token=abc",
	}

	for _, input := range tests {
		nodes, err := ParseGeneralSubscription([]byte(input))
		if err != nil {
			t.Fatalf("input %q should not return error, got %v", input, err)
		}
		if len(nodes) != 0 {
			t.Fatalf("input %q should not be parsed as proxy node, got %d", input, len(nodes))
		}
	}
}
func TestParseGeneralSubscription_PlainHTTPProxyLines(t *testing.T) {
	data := []byte(`
1.2.3.4:8080
5.6.7.8:3128:user-a:pass-a
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

	if first["type"] != "http" {
		t.Fatalf("expected first type http, got %v", first["type"])
	}
	if first["server"] != "1.2.3.4" {
		t.Fatalf("expected first server 1.2.3.4, got %v", first["server"])
	}
	if first["server_port"] != float64(8080) {
		t.Fatalf("expected first server_port 8080, got %v", first["server_port"])
	}
	if _, ok := first["username"]; ok {
		t.Fatalf("expected first proxy without username, got %v", first["username"])
	}
	if _, ok := first["password"]; ok {
		t.Fatalf("expected first proxy without password, got %v", first["password"])
	}

	if second["type"] != "http" {
		t.Fatalf("expected second type http, got %v", second["type"])
	}
	if second["server"] != "5.6.7.8" {
		t.Fatalf("expected second server 5.6.7.8, got %v", second["server"])
	}
	if second["server_port"] != float64(3128) {
		t.Fatalf("expected second server_port 3128, got %v", second["server_port"])
	}
	if second["username"] != "user-a" {
		t.Fatalf("expected second username user-a, got %v", second["username"])
	}
	if second["password"] != "pass-a" {
		t.Fatalf("expected second password pass-a, got %v", second["password"])
	}
}

func TestParseGeneralSubscription_PlainHTTPProxyLinesIPv6(t *testing.T) {
	data := []byte(`
[2001:db8::1]:8080
2001:db8::2:3128:user-v6:pass-v6
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

	if first["type"] != "http" {
		t.Fatalf("expected first type http, got %v", first["type"])
	}
	if first["server"] != "2001:db8::1" {
		t.Fatalf("expected first server 2001:db8::1, got %v", first["server"])
	}
	if first["server_port"] != float64(8080) {
		t.Fatalf("expected first server_port 8080, got %v", first["server_port"])
	}

	if second["type"] != "http" {
		t.Fatalf("expected second type http, got %v", second["type"])
	}
	if second["server"] != "2001:db8::2" {
		t.Fatalf("expected second server 2001:db8::2, got %v", second["server"])
	}
	if second["server_port"] != float64(3128) {
		t.Fatalf("expected second server_port 3128, got %v", second["server_port"])
	}
	if second["username"] != "user-v6" {
		t.Fatalf("expected second username user-v6, got %v", second["username"])
	}
	if second["password"] != "pass-v6" {
		t.Fatalf("expected second password pass-v6, got %v", second["password"])
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

func TestParseGeneralSubscription_ClashJSON_SSCipherAliasNormalized(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "ss-alias",
				"type": "ss",
				"server": "1.1.1.1",
				"port": 8388,
				"cipher": "AEAD_CHACHA20_POLY1305",
				"password": "pass"
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
	if got := obj["method"]; got != "chacha20-ietf-poly1305" {
		t.Fatalf("method: got %v", got)
	}
}

func TestParseGeneralSubscription_ClashJSON_VMessWebSocketAliasNetwork(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{
				"name": "vmess-websocket",
				"type": "vmess",
				"server": "example.com",
				"port": 443,
				"uuid": "11111111-2222-3333-4444-555555555555",
				"network": "websocket",
				"ws-opts": {
					"path": "/ws",
					"headers": {"Host": "ws.example.com"}
				}
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
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "ws" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/ws" {
		t.Fatalf("transport.path: got %v", got)
	}
}

func TestParseGeneralSubscription_VMessURIAllowInsecureFalseDoesNotForceTLS(t *testing.T) {
	vmessPayload := `{"v":"2","ps":"vmess-no-tls","add":"example.com","port":"443","id":"11111111-2222-3333-4444-555555555555","aid":"0","net":"tcp","allowInsecure":"0"}`
	data := []byte("vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessPayload)))
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if _, ok := obj["tls"]; ok {
		t.Fatalf("tls should be absent when allowInsecure=false and tls is not enabled, got %v", obj["tls"])
	}
}

func TestParseGeneralSubscription_VMessURIQUICTransportNotDowngraded(t *testing.T) {
	data := []byte("vmess://quic+tls:11111111-2222-3333-4444-555555555555-0@example.com:443?security=aes-128-gcm&type=srtp&key=abc#vmess-quic")
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "quic" {
		t.Fatalf("transport.type: got %v", got)
	}
}

func TestParseGeneralSubscription_SurgeProxySection_HTTPNetworkUsesHTTPOpts(t *testing.T) {
	data := []byte(`
[Proxy]
vmess-http = vmess, 1.2.3.4, 443, username=11111111-2222-3333-4444-555555555555, tls=true, network=http, path=/x, host=h.example.com
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	transport := mustMapField(t, obj, "transport")
	if got := transport["type"]; got != "http" {
		t.Fatalf("transport.type: got %v", got)
	}
	if got := transport["path"]; got != "/x" {
		t.Fatalf("transport.path: got %v", got)
	}
	hosts := mustSliceField(t, transport, "host")
	if !containsAnyString(hosts, "h.example.com") {
		t.Fatalf("transport.host: got %v", hosts)
	}
}

func TestParseGeneralSubscription_SurgeProxySection_WireGuardSectionName(t *testing.T) {
	data := []byte(`
[Proxy]
wg = wireguard, section-name=test

[WireGuard test]
self-ip = 172.16.0.2/32
private-key = priv-key
peer = (public-key = pub-key, allowed-ips = "0.0.0.0/0, ::/0", endpoint = engage.cloudflareclient.com:2408)
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "wireguard" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["server"]; got != "engage.cloudflareclient.com" {
		t.Fatalf("server: got %v", got)
	}
	if got := obj["server_port"]; got != float64(2408) {
		t.Fatalf("server_port: got %v", got)
	}
	if got := obj["private_key"]; got != "priv-key" {
		t.Fatalf("private_key: got %v", got)
	}
	if got := obj["peer_public_key"]; got != "pub-key" {
		t.Fatalf("peer_public_key: got %v", got)
	}
	localAddress := mustSliceField(t, obj, "local_address")
	if !containsAnyString(localAddress, "172.16.0.2/32") {
		t.Fatalf("local_address: got %v", localAddress)
	}
}

func TestParseGeneralSubscription_ProxyURILinesLegacyAndTelegram(t *testing.T) {
	data := []byte(`
socks://dXNlcjpwYXNzQDEuMS4xLjE6MTA4MA==#Legacy%20SOCKS
tg://socks?server=2.2.2.2&port=1081&user=tguser&pass=tgpass&remarks=TG%20SOCKS
tg://http?server=3.3.3.3&port=8080&user=httpuser&pass=httppass&remarks=TG%20HTTP
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 parsed nodes, got %d", len(nodes))
	}
	byTag := parseNodesByTag(t, nodes)

	legacy := byTag["Legacy SOCKS"]
	if got := legacy["type"]; got != "socks" {
		t.Fatalf("legacy type: got %v", got)
	}
	if got := legacy["username"]; got != "user" {
		t.Fatalf("legacy username: got %v", got)
	}
	if got := legacy["password"]; got != "pass" {
		t.Fatalf("legacy password: got %v", got)
	}

	tgSocks := byTag["TG SOCKS"]
	if got := tgSocks["type"]; got != "socks" {
		t.Fatalf("tg socks type: got %v", got)
	}
	if got := tgSocks["server"]; got != "2.2.2.2" {
		t.Fatalf("tg socks server: got %v", got)
	}

	tgHTTP := byTag["TG HTTP"]
	if got := tgHTTP["type"]; got != "http" {
		t.Fatalf("tg http type: got %v", got)
	}
	if got := tgHTTP["username"]; got != "httpuser" {
		t.Fatalf("tg http username: got %v", got)
	}
}

func TestParseGeneralSubscription_TelegramProxyURIInvalidDecimalPortIsRejected(t *testing.T) {
	data := []byte("tg://socks?server=2.2.2.2&port=1080.5&user=tguser&pass=tgpass&remarks=TG%20SOCKS")
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 parsed nodes for invalid decimal port, got %d", len(nodes))
	}
}

func TestParseGeneralSubscription_NetchURI_SS(t *testing.T) {
	payload := `{"Type":"SS","Remark":"Netch SS","Hostname":"1.1.1.1","Port":8388,"EncryptMethod":"AEAD_CHACHA20_POLY1305","Password":"pass"}`
	data := []byte("Netch://" + base64.StdEncoding.EncodeToString([]byte(payload)))
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "shadowsocks" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["method"]; got != "chacha20-ietf-poly1305" {
		t.Fatalf("method: got %v", got)
	}
}

func TestParseGeneralSubscription_NetchURI_SSWithObfsAlias(t *testing.T) {
	payload := `{"Type":"SS","Remark":"Netch SS Obfs","Hostname":"1.1.1.1","Port":8388,"EncryptMethod":"AEAD_CHACHA20_POLY1305","Password":"pass","Plugin":"simple-obfs","PluginOption":"mode=http;host=obfs.example.com"}`
	data := []byte("Netch://" + base64.StdEncoding.EncodeToString([]byte(payload)))

	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}

	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["plugin"]; got != "obfs-local" {
		t.Fatalf("plugin: got %v", got)
	}
	if got := obj["plugin_opts"]; got != "obfs=http;obfs-host=obfs.example.com" {
		t.Fatalf("plugin_opts: got %v", got)
	}
}

func TestParseGeneralSubscription_NetchURILowercaseScheme(t *testing.T) {
	payload := `{"Type":"SS","Remark":"netch-lower","Hostname":"1.1.1.1","Port":8388,"EncryptMethod":"AEAD_CHACHA20_POLY1305","Password":"pass"}`
	data := []byte("netch://" + base64.StdEncoding.EncodeToString([]byte(payload)))
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 parsed node, got %d", len(nodes))
	}
	obj := parseNodeRaw(t, nodes[0].RawOptions)
	if got := obj["type"]; got != "shadowsocks" {
		t.Fatalf("type: got %v", got)
	}
	if got := obj["tag"]; got != "netch-lower" {
		t.Fatalf("tag: got %v", got)
	}
}

func TestParseGeneralSubscription_SurgeProxySection_QXAndCustom(t *testing.T) {
	data := []byte(`
[Proxy]
shadowsocks = 1.1.1.1:8388, method=aes-128-gcm, password=ss-pass, tag=qx-ss
vmess = 2.2.2.2:443, method=auto, password=11111111-2222-3333-4444-555555555555, obfs=ws, obfs-host=ws.example.com, obfs-uri=/ws, over-tls=true, tag=qx-vmess
custom-ss = custom, 3.3.3.3, 8388, aes-256-gcm, custom-pass
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 parsed nodes, got %d", len(nodes))
	}
	byTag := parseNodesByTag(t, nodes)

	if got := byTag["qx-ss"]["type"]; got != "shadowsocks" {
		t.Fatalf("qx-ss type: got %v", got)
	}
	qxVMess := byTag["qx-vmess"]
	if got := qxVMess["type"]; got != "vmess" {
		t.Fatalf("qx-vmess type: got %v", got)
	}
	qxVMessTransport := mustMapField(t, qxVMess, "transport")
	if got := qxVMessTransport["type"]; got != "ws" {
		t.Fatalf("qx-vmess transport.type: got %v", got)
	}
	if got := byTag["custom-ss"]["type"]; got != "shadowsocks" {
		t.Fatalf("custom-ss type: got %v", got)
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

func parseNodesByTag(t *testing.T, nodes []ParsedNode) map[string]map[string]any {
	t.Helper()
	byTag := make(map[string]map[string]any, len(nodes))
	for _, node := range nodes {
		obj := parseNodeRaw(t, node.RawOptions)
		tag, _ := obj["tag"].(string)
		byTag[tag] = obj
	}
	return byTag
}

func mustMapField(t *testing.T, obj map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := obj[key]
	if !ok {
		t.Fatalf("missing map field %q", key)
	}
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("field %q expected map[string]any, got %T", key, value)
	}
	return out
}

func mustSliceField(t *testing.T, obj map[string]any, key string) []any {
	t.Helper()
	value, ok := obj[key]
	if !ok {
		t.Fatalf("missing slice field %q", key)
	}
	out, ok := value.([]any)
	if !ok {
		t.Fatalf("field %q expected []any, got %T", key, value)
	}
	return out
}

func containsAnyString(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
