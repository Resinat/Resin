package service

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"gopkg.in/yaml.v3"
)

const (
	PublicSubscriptionFormatClash   = "clash"
	PublicSubscriptionFormatV2Ray   = "v2ray"
	PublicSubscriptionFormatSingBox = "sing-box"
)

type PublicSubscriptionResult struct {
	Body        []byte
	ContentType string
	Skipped     int
	Usage       subscription.UsageInfo
}

type publicSubscriptionNode struct {
	hash       node.Hash
	raw        json.RawMessage
	name       string
	latencyMs  float64
	hasLatency bool
}

// RenderPublicSubscription validates the public token and renders the
// subscription in one of the supported client formats.
func (s *ControlPlaneService) RenderPublicSubscription(id, token, format string) (*PublicSubscriptionResult, error) {
	sub := s.SubMgr.Lookup(id)
	if sub == nil || !publicTokenEqual(sub.PublicToken(), token) || !sub.Enabled() {
		return nil, notFound("subscription not found")
	}

	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" || format == "auto" {
		format = PublicSubscriptionFormatClash
	}
	if format == "mihomo" {
		format = PublicSubscriptionFormatClash
	}
	if format == "singbox" {
		format = PublicSubscriptionFormatSingBox
	}
	if format != PublicSubscriptionFormatClash &&
		format != PublicSubscriptionFormatV2Ray &&
		format != PublicSubscriptionFormatSingBox {
		return nil, invalidArg("format: must be auto, clash, mihomo, v2ray, or sing-box")
	}

	nodes := s.publicSubscriptionNodes(sub)
	result := &PublicSubscriptionResult{Usage: sub.Usage()}
	switch format {
	case PublicSubscriptionFormatSingBox:
		result.ContentType = "application/json; charset=utf-8"
		result.Body, result.Skipped = renderSingBoxSubscription(nodes)
	case PublicSubscriptionFormatClash:
		result.ContentType = "application/yaml; charset=utf-8"
		result.Body, result.Skipped = renderClashSubscription(nodes)
	case PublicSubscriptionFormatV2Ray:
		result.ContentType = "text/plain; charset=utf-8"
		result.Body, result.Skipped = renderV2RaySubscription(nodes)
	}
	return result, nil
}

func publicTokenEqual(expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func (s *ControlPlaneService) publicSubscriptionNodes(sub *subscription.Subscription) []publicSubscriptionNode {
	if sub == nil || s == nil || s.Pool == nil || !sub.Enabled() {
		return nil
	}

	var authorities []string
	if s.RuntimeCfg != nil {
		if cfg := s.RuntimeCfg.Load(); cfg != nil {
			authorities = cfg.LatencyAuthorities
		}
	}

	result := make([]publicSubscriptionNode, 0, sub.ManagedNodes().Size())
	sub.ManagedNodes().RangeNodes(func(h node.Hash, managed subscription.ManagedNode) bool {
		if managed.Evicted {
			return true
		}
		entry, ok := s.Pool.GetEntry(h)
		if !ok || entry == nil || entry.IsManuallyDisabled() || !entry.IsHealthy() {
			return true
		}
		if !entry.GetEgressIP().IsValid() || !entry.HasLatency() {
			return true
		}

		name := smallestTag(managed.Tags)
		if name == "" {
			name = h.Hex()
		}
		candidate := publicSubscriptionNode{
			hash: h,
			raw:  append(json.RawMessage(nil), entry.RawOptions...),
			name: name,
		}
		if avg, ok := node.AverageEWMAForDomainsMs(entry, authorities); ok {
			candidate.latencyMs = avg
			candidate.hasLatency = true
		}
		result = append(result, candidate)
		return true
	})

	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.hasLatency != b.hasLatency {
			return a.hasLatency
		}
		if a.hasLatency && a.latencyMs != b.latencyMs {
			return a.latencyMs < b.latencyMs
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.hash.Hex() < b.hash.Hex()
	})
	return result
}

func smallestTag(tags []string) string {
	best := ""
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && (best == "" || tag < best) {
			best = tag
		}
	}
	return best
}

func withTag(raw json.RawMessage, name string) (json.RawMessage, bool) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	object["tag"] = name
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func renderSingBoxSubscription(nodes []publicSubscriptionNode) ([]byte, int) {
	outbounds := make([]json.RawMessage, 0, len(nodes))
	skipped := 0
	for _, item := range nodes {
		raw, ok := withTag(item.raw, item.name)
		if !ok {
			skipped++
			continue
		}
		outbounds = append(outbounds, raw)
	}
	body, err := json.MarshalIndent(map[string]any{"outbounds": outbounds}, "", "  ")
	if err != nil {
		return []byte(`{"outbounds":[]}`), skipped
	}
	return append(body, '\n'), skipped
}

type clashDNSConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Nameserver []string `yaml:"nameserver"`
	Fallback   []string `yaml:"fallback"`
}

type clashProxyGroup struct {
	Name      string   `yaml:"name"`
	Type      string   `yaml:"type"`
	URL       string   `yaml:"url,omitempty"`
	Interval  int      `yaml:"interval,omitempty"`
	Tolerance int      `yaml:"tolerance,omitempty"`
	Proxies   []string `yaml:"proxies"`
}

type clashSubscriptionConfig struct {
	Port               int               `yaml:"port"`
	SocksPort          int               `yaml:"socks-port"`
	AllowLAN           bool              `yaml:"allow-lan"`
	Mode               string            `yaml:"mode"`
	LogLevel           string            `yaml:"log-level"`
	ExternalController string            `yaml:"external-controller"`
	DNS                clashDNSConfig    `yaml:"dns"`
	Proxies            []map[string]any  `yaml:"proxies"`
	ProxyGroups        []clashProxyGroup `yaml:"proxy-groups"`
	Rules              []string          `yaml:"rules"`
}

func renderClashSubscription(nodes []publicSubscriptionNode) ([]byte, int) {
	proxies := make([]map[string]any, 0, len(nodes))
	proxyNames := make([]string, 0, len(nodes))
	skipped := 0
	for _, item := range nodes {
		proxy, ok := clashProxyFromRaw(item.raw, item.name)
		if !ok {
			skipped++
			continue
		}
		proxies = append(proxies, proxy)
		proxyNames = append(proxyNames, item.name)
	}
	config := clashSubscriptionConfig{
		Port:               7890,
		SocksPort:          7891,
		AllowLAN:           true,
		Mode:               "Rule",
		LogLevel:           "info",
		ExternalController: ":9090",
		DNS: clashDNSConfig{
			Enabled:    true,
			Nameserver: []string{"119.29.29.29", "223.5.5.5"},
			Fallback:   []string{"8.8.8.8", "8.8.4.4", "tls://1.0.0.1:853", "tls://dns.google:853"},
		},
		Proxies: proxies,
		ProxyGroups: []clashProxyGroup{
			{
				Name:    "🚀 节点选择",
				Type:    "select",
				Proxies: append([]string{"♻️ 自动选择", "DIRECT"}, proxyNames...),
			},
			{
				Name:      "♻️ 自动选择",
				Type:      "url-test",
				URL:       "http://www.gstatic.com/generate_204",
				Interval:  300,
				Tolerance: 50,
				Proxies:   append([]string{}, proxyNames...),
			},
			{
				Name:    "🎯 全球直连",
				Type:    "select",
				Proxies: []string{"DIRECT", "🚀 节点选择", "♻️ 自动选择"},
			},
			{
				Name:    "🛑 全球拦截",
				Type:    "select",
				Proxies: []string{"REJECT", "DIRECT"},
			},
			{
				Name:    "🐟 漏网之鱼",
				Type:    "select",
				Proxies: append([]string{"🚀 节点选择", "🎯 全球直连", "♻️ 自动选择"}, proxyNames...),
			},
		},
		Rules: []string{
			"GEOIP,CN,🎯 全球直连",
			"MATCH,🐟 漏网之鱼",
		},
	}
	body, err := yaml.Marshal(config)
	if err != nil {
		return []byte("proxies: []\n"), skipped
	}
	return body, skipped
}

func renderV2RaySubscription(nodes []publicSubscriptionNode) ([]byte, int) {
	lines := make([]string, 0, len(nodes))
	skipped := 0
	for _, item := range nodes {
		line, ok := v2RayURIFromRaw(item.raw, item.name)
		if !ok {
			skipped++
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []byte{}, skipped
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\n") + "\n"))), skipped
}

func rawObject(raw json.RawMessage) (map[string]any, bool) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func stringField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uintField(object map[string]any, keys ...string) (uint64, bool) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		switch n := value.(type) {
		case float64:
			if n > 0 && n == float64(uint64(n)) {
				return uint64(n), true
			}
		case json.Number:
			v, err := strconv.ParseUint(n.String(), 10, 16)
			if err == nil && v > 0 {
				return v, true
			}
		case string:
			v, err := strconv.ParseUint(strings.TrimSpace(n), 10, 16)
			if err == nil && v > 0 {
				return v, true
			}
		}
	}
	return 0, false
}

func mapField(object map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value, ok := object[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func boolField(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := object[key].(bool); ok {
			return value
		}
	}
	return false
}

func copyField(dst map[string]any, src map[string]any, dstKey string, srcKeys ...string) {
	if value := stringField(src, srcKeys...); value != "" {
		dst[dstKey] = value
	}
}

func baseClashProxy(object map[string]any, name, typ string) (map[string]any, bool) {
	server := stringField(object, "server")
	port, ok := uintField(object, "server_port", "port")
	if server == "" || !ok {
		return nil, false
	}
	return map[string]any{
		"name":   name,
		"type":   typ,
		"server": server,
		"port":   port,
	}, true
}

func applyClashTLS(dst map[string]any, object map[string]any, nameField string) {
	tls := mapField(object, "tls")
	if tls == nil || !boolField(tls, "enabled") {
		return
	}
	dst["tls"] = true
	if sni := stringField(tls, "server_name"); sni != "" {
		dst[nameField] = sni
	}
	if boolField(tls, "insecure") {
		dst["skip-cert-verify"] = true
	}
	if value, ok := tls["alpn"].([]any); ok && len(value) > 0 {
		alpn := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && text != "" {
				alpn = append(alpn, text)
			}
		}
		if len(alpn) > 0 {
			dst["alpn"] = alpn
		}
	}
	if reality := mapField(tls, "reality"); reality != nil && boolField(reality, "enabled") {
		realityOpts := map[string]any{}
		copyField(realityOpts, reality, "public-key", "public_key")
		copyField(realityOpts, reality, "short-id", "short_id")
		if len(realityOpts) > 0 {
			dst["reality-opts"] = realityOpts
		}
	}
	if utls := mapField(tls, "utls"); utls != nil {
		copyField(dst, utls, "client-fingerprint", "fingerprint")
	}
}

func applyClashTransport(dst map[string]any, object map[string]any) {
	transport := mapField(object, "transport")
	if transport == nil {
		return
	}
	typ := strings.ToLower(stringField(transport, "type"))
	switch typ {
	case "ws":
		dst["network"] = "ws"
		opts := map[string]any{}
		copyField(opts, transport, "path", "path")
		if headers := mapField(transport, "headers"); headers != nil {
			opts["headers"] = headers
		}
		if len(opts) > 0 {
			dst["ws-opts"] = opts
		}
	case "grpc":
		dst["network"] = "grpc"
		if serviceName := stringField(transport, "service_name"); serviceName != "" {
			dst["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "http", "httpupgrade", "quic":
		dst["network"] = typ
		opts := map[string]any{}
		copyField(opts, transport, "path", "path")
		copyField(opts, transport, "service-name", "service_name")
		if hosts := stringListField(transport, "host"); len(hosts) > 0 {
			opts["headers"] = map[string]any{"Host": hosts}
		}
		if len(opts) > 0 {
			dst["http-opts"] = opts
		}
	}
}

func copyClashScalar(dst map[string]any, src map[string]any, dstKey string, srcKeys ...string) {
	for _, key := range srcKeys {
		if value, ok := src[key]; ok {
			switch value := value.(type) {
			case string:
				if strings.TrimSpace(value) != "" {
					dst[dstKey] = value
				}
			case float64, json.Number, bool:
				dst[dstKey] = value
			}
			return
		}
	}
}

func clashProxyFromRaw(raw json.RawMessage, name string) (map[string]any, bool) {
	object, ok := rawObject(raw)
	if !ok {
		return nil, false
	}
	typ := strings.ToLower(stringField(object, "type"))
	switch typ {
	case "http":
		proxy, ok := baseClashProxy(object, name, "http")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "username", "username")
		copyField(proxy, object, "password", "password")
		if headers := mapField(object, "headers"); headers != nil {
			proxy["headers"] = headers
		}
		applyClashTLS(proxy, object, "sni")
		return proxy, true
	case "socks", "socks5":
		proxy, ok := baseClashProxy(object, name, "socks5")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "username", "username")
		copyField(proxy, object, "password", "password")
		return proxy, true
	case "shadowsocks":
		proxy, ok := baseClashProxy(object, name, "ss")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "cipher", "method")
		copyField(proxy, object, "password", "password")
		copyField(proxy, object, "plugin", "plugin")
		copyField(proxy, object, "plugin-opts", "plugin_opts")
		return proxy, stringField(proxy, "cipher") != "" && stringField(proxy, "password") != ""
	case "vmess":
		proxy, ok := baseClashProxy(object, name, "vmess")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "uuid", "uuid")
		copyField(proxy, object, "cipher", "security")
		if alterID, exists := uintField(object, "alter_id"); exists {
			proxy["alterId"] = alterID
		}
		applyClashTLS(proxy, object, "servername")
		applyClashTransport(proxy, object)
		return proxy, stringField(proxy, "uuid") != ""
	case "vless":
		proxy, ok := baseClashProxy(object, name, "vless")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "uuid", "uuid")
		copyField(proxy, object, "flow", "flow")
		applyClashTLS(proxy, object, "servername")
		applyClashTransport(proxy, object)
		return proxy, stringField(proxy, "uuid") != ""
	case "trojan":
		proxy, ok := baseClashProxy(object, name, "trojan")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "password", "password")
		applyClashTLS(proxy, object, "sni")
		applyClashTransport(proxy, object)
		return proxy, stringField(proxy, "password") != ""
	case "hysteria", "hysteria2":
		proxy, ok := baseClashProxy(object, name, typ)
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "password", "password")
		copyField(proxy, object, "auth-str", "auth_str")
		copyClashScalar(proxy, object, "up", "up_mbps")
		copyClashScalar(proxy, object, "down", "down_mbps")
		copyField(proxy, object, "hop-interval", "hop_interval")
		if ports := stringListField(object, "server_ports"); len(ports) > 0 {
			proxy["ports"] = strings.Join(ports, ":")
		}
		if obfs := mapField(object, "obfs"); obfs != nil {
			copyField(proxy, obfs, "obfs", "type")
			copyField(proxy, obfs, "obfs-password", "password")
		}
		applyClashTLS(proxy, object, "sni")
		return proxy, stringField(proxy, "password", "auth-str") != ""
	case "tuic":
		proxy, ok := baseClashProxy(object, name, "tuic")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "uuid", "uuid")
		copyField(proxy, object, "password", "password")
		copyField(proxy, object, "congestion-controller", "congestion_control")
		copyField(proxy, object, "udp-relay-mode", "udp_relay_mode")
		if boolField(object, "zero_rtt_handshake") {
			proxy["reduce-rtt"] = true
		}
		applyClashTLS(proxy, object, "sni")
		return proxy, stringField(proxy, "uuid") != ""
	case "wireguard":
		proxy, ok := baseClashProxy(object, name, "wireguard")
		if !ok {
			return nil, false
		}
		copyField(proxy, object, "private-key", "private_key")
		copyField(proxy, object, "public-key", "peer_public_key", "public_key")
		if addresses := stringListField(object, "local_address"); len(addresses) > 0 {
			proxy["ip"] = addresses[0]
			if len(addresses) > 1 {
				proxy["ipv6"] = addresses[1]
			}
		}
		if peers, ok := object["peers"].([]any); ok && len(peers) > 0 {
			if peer, ok := peers[0].(map[string]any); ok {
				if values := stringListField(peer, "allowed_ips"); len(values) > 0 {
					proxy["allowed-ips"] = values
				}
				copyField(proxy, peer, "pre-shared-key", "pre_shared_key")
			}
		}
		copyClashScalar(proxy, object, "mtu", "mtu")
		return proxy, stringField(proxy, "private-key") != "" && stringField(proxy, "public-key") != "" && stringField(proxy, "ip") != ""
	case "anytls", "naive", "ssh", "tor", "shadowtls":
		return nil, false
	default:
		return nil, false
	}
}

func v2RayURIFromRaw(raw json.RawMessage, name string) (string, bool) {
	object, ok := rawObject(raw)
	if !ok {
		return "", false
	}
	typ := strings.ToLower(stringField(object, "type"))
	server := stringField(object, "server")
	port, portOK := uintField(object, "server_port", "port")
	if server == "" || !portOK {
		return "", false
	}
	switch typ {
	case "http":
		u := url.URL{Scheme: "http", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), Fragment: name}
		if username := stringField(object, "username"); username != "" {
			u.User = url.UserPassword(username, stringField(object, "password"))
		}
		if tls := mapField(object, "tls"); tls != nil && boolField(tls, "enabled") {
			u.Scheme = "https"
			q := url.Values{}
			applyCommonV2RayQueryTLS(q, tls)
			u.RawQuery = q.Encode()
		}
		return u.String(), true
	case "socks":
		u := url.URL{Scheme: "socks", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), Fragment: name}
		username := stringField(object, "username")
		password := stringField(object, "password")
		if username != "" || password != "" {
			encoded := base64.RawStdEncoding.EncodeToString([]byte(username + ":" + password))
			u.User = url.User(encoded)
		}
		return u.String(), true
	case "shadowsocks":
		method, password := stringField(object, "method"), stringField(object, "password")
		if method == "" || password == "" {
			return "", false
		}
		encoded := base64.RawStdEncoding.EncodeToString([]byte(method + ":" + password))
		u := url.URL{Scheme: "ss", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10))}
		u.User = url.User(encoded)
		u.Fragment = name
		if plugin := stringField(object, "plugin"); plugin != "" {
			q := url.Values{"plugin": {plugin}}
			if opts := stringField(object, "plugin_opts", "plugin-opts"); opts != "" {
				q.Set("plugin", plugin+";"+opts)
			}
			u.RawQuery = q.Encode()
		}
		return u.String(), true
	case "vmess":
		uuid := stringField(object, "uuid")
		if uuid == "" {
			return "", false
		}
		payload := map[string]any{"v": "2", "ps": name, "add": server, "port": strconv.FormatUint(port, 10), "id": uuid, "aid": uintValue(object, "alter_id"), "scy": stringField(object, "security"), "net": "tcp", "type": "none"}
		applyV2RayTransport(payload, object)
		applyV2RayTLS(payload, object)
		body, err := json.Marshal(payload)
		if err != nil {
			return "", false
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(body), true
	case "vless":
		uuid := stringField(object, "uuid")
		if uuid == "" {
			return "", false
		}
		u := url.URL{Scheme: "vless", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.User(uuid), Fragment: name}
		query := url.Values{"encryption": {"none"}}
		applyV2RayQueryTransport(query, object)
		applyV2RayQueryTLS(query, object)
		u.RawQuery = query.Encode()
		return u.String(), true
	case "trojan":
		password := stringField(object, "password")
		if password == "" {
			return "", false
		}
		u := url.URL{Scheme: "trojan", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.User(password), Fragment: name}
		query := url.Values{}
		applyV2RayQueryTransport(query, object)
		applyV2RayQueryTLS(query, object)
		u.RawQuery = query.Encode()
		return u.String(), true
	case "hysteria2":
		password := stringField(object, "password")
		if password == "" {
			return "", false
		}
		u := url.URL{Scheme: "hysteria2", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.User(password), Fragment: name}
		query := url.Values{}
		applyCommonV2RayQueryTLS(query, mapField(object, "tls"))
		if tls := mapField(object, "tls"); tls != nil && boolField(tls, "insecure") {
			query.Set("insecure", "1")
		}
		if obfs := mapField(object, "obfs"); obfs != nil {
			copyStringQuery(query, "obfs", obfs, "type")
			copyStringQuery(query, "obfs-password", obfs, "password")
		}
		if ports := stringListField(object, "server_ports"); len(ports) > 0 {
			query.Set("mport", strings.ReplaceAll(strings.Join(ports, ":"), ":", "-"))
		}
		copyScalarQuery(query, "upmbps", object, "up_mbps")
		copyScalarQuery(query, "downmbps", object, "down_mbps")
		copyStringQuery(query, "hopInterval", object, "hop_interval")
		u.RawQuery = query.Encode()
		return u.String(), true
	case "tuic":
		uuid := stringField(object, "uuid")
		if uuid == "" {
			return "", false
		}
		u := url.URL{Scheme: "tuic", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), Fragment: name}
		if password := stringField(object, "password"); password != "" {
			u.User = url.UserPassword(uuid, password)
		} else {
			u.User = url.User(uuid)
		}
		query := url.Values{}
		applyCommonV2RayQueryTLS(query, mapField(object, "tls"))
		if tls := mapField(object, "tls"); tls != nil && boolField(tls, "insecure") {
			query.Set("allow_insecure", "1")
		}
		copyStringQuery(query, "congestion_control", object, "congestion_control")
		copyStringQuery(query, "udp_relay_mode", object, "udp_relay_mode")
		if boolField(object, "zero_rtt_handshake") {
			query.Set("zero_rtt_handshake", "1")
		}
		u.RawQuery = query.Encode()
		return u.String(), true
	case "wireguard":
		privateKey := stringField(object, "private_key")
		publicKey := stringField(object, "peer_public_key", "public_key")
		if privateKey == "" || publicKey == "" {
			return "", false
		}
		u := url.URL{Scheme: "wireguard", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.User(privateKey), Fragment: name}
		query := url.Values{"publickey": {publicKey}}
		copyStringQuery(query, "presharedkey", object, "pre_shared_key")
		if values := stringListField(object, "reserved"); len(values) > 0 {
			query.Set("reserved", strings.Join(values, ","))
		}
		if values := stringListField(object, "local_address"); len(values) > 0 {
			query.Set("address", strings.Join(values, ","))
		}
		copyScalarQuery(query, "mtu", object, "mtu")
		u.RawQuery = query.Encode()
		return u.String(), true
	case "anytls":
		password := stringField(object, "password")
		if password == "" {
			return "", false
		}
		u := url.URL{Scheme: "anytls", Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.User(password), Fragment: name}
		query := url.Values{}
		applyCommonV2RayQueryTLS(query, mapField(object, "tls"))
		if tls := mapField(object, "tls"); tls != nil && boolField(tls, "insecure") {
			query.Set("insecure", "1")
		}
		u.RawQuery = query.Encode()
		return u.String(), true
	case "naive":
		username := stringField(object, "username")
		password := stringField(object, "password")
		if username == "" || password == "" {
			return "", false
		}
		scheme := "naive+https"
		if boolField(object, "quic") {
			scheme = "naive+quic"
		}
		u := url.URL{Scheme: scheme, Host: net.JoinHostPort(server, strconv.FormatUint(port, 10)), User: url.UserPassword(username, password), Fragment: name}
		query := url.Values{}
		copyScalarQuery(query, "insecure-concurrency", object, "insecure_concurrency")
		copyStringQuery(query, "quic_congestion_control", object, "quic_congestion_control")
		if boolField(object, "udp_over_tcp") {
			query.Set("udp_over_tcp", "1")
		}
		u.RawQuery = query.Encode()
		return u.String(), true
	default:
		return "", false
	}
}

func copyStringQuery(query url.Values, dst string, object map[string]any, src string) {
	if object == nil {
		return
	}
	if value := stringField(object, src); value != "" {
		query.Set(dst, value)
	}
}

func copyScalarQuery(query url.Values, dst string, object map[string]any, src string) {
	if object == nil {
		return
	}
	value, ok := object[src]
	if !ok {
		return
	}
	switch value := value.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			query.Set(dst, value)
		}
	case float64:
		query.Set(dst, strconv.FormatFloat(value, 'f', -1, 64))
	case json.Number:
		query.Set(dst, value.String())
	}
}

func stringListField(object map[string]any, keys ...string) []string {
	if object == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		var result []string
		switch values := value.(type) {
		case []any:
			for _, item := range values {
				if text := strings.TrimSpace(stringifyScalar(item)); text != "" {
					result = append(result, text)
				}
			}
		case []string:
			for _, item := range values {
				if text := strings.TrimSpace(item); text != "" {
					result = append(result, text)
				}
			}
		case string:
			if text := strings.TrimSpace(values); text != "" {
				result = []string{text}
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return nil
}

func stringifyScalar(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func applyCommonV2RayQueryTLS(query url.Values, tls map[string]any) {
	if tls == nil || !boolField(tls, "enabled") {
		return
	}
	if sni := stringField(tls, "server_name"); sni != "" {
		query.Set("sni", sni)
	}
	if boolField(tls, "insecure") {
		query.Set("insecure", "1")
	}
	if values := stringListField(tls, "alpn"); len(values) > 0 {
		query.Set("alpn", strings.Join(values, ","))
	}
	if utls := mapField(tls, "utls"); utls != nil {
		copyStringQuery(query, "fp", utls, "fingerprint")
	}
	if reality := mapField(tls, "reality"); reality != nil && boolField(reality, "enabled") {
		query.Set("security", "reality")
		copyStringQuery(query, "pbk", reality, "public_key")
		copyStringQuery(query, "sid", reality, "short_id")
	}
}

func uintValue(object map[string]any, key string) uint64 {
	value, _ := uintField(object, key)
	return value
}

func transportInfo(object map[string]any) (map[string]any, string) {
	transport := mapField(object, "transport")
	if transport == nil {
		return nil, "tcp"
	}
	return transport, strings.ToLower(stringField(transport, "type"))
}

func applyV2RayTransport(payload map[string]any, object map[string]any) {
	transport, typ := transportInfo(object)
	if transport == nil {
		return
	}
	switch typ {
	case "ws":
		payload["net"] = "ws"
		if path := stringField(transport, "path"); path != "" {
			payload["path"] = path
		}
		if headers := mapField(transport, "headers"); headers != nil {
			if host := stringField(headers, "Host", "host"); host != "" {
				payload["host"] = host
			}
		}
	case "grpc":
		payload["net"] = "grpc"
		payload["type"] = "none"
		if service := stringField(transport, "service_name"); service != "" {
			payload["path"] = service
		}
	case "http":
		payload["net"] = "http"
		payload["type"] = "http"
		if path := stringField(transport, "path"); path != "" {
			payload["path"] = path
		}
	}
}

func applyV2RayTLS(payload map[string]any, object map[string]any) {
	tls := mapField(object, "tls")
	if tls == nil || !boolField(tls, "enabled") {
		return
	}
	payload["tls"] = "tls"
	if sni := stringField(tls, "server_name"); sni != "" {
		payload["sni"] = sni
	}
	if boolField(tls, "insecure") {
		payload["allowInsecure"] = "1"
	}
}

func applyV2RayQueryTransport(query url.Values, object map[string]any) {
	transport, typ := transportInfo(object)
	if transport == nil || typ == "" || typ == "tcp" {
		return
	}
	if typ == "ws" {
		query.Set("type", "ws")
	} else {
		query.Set("type", typ)
	}
	if path := stringField(transport, "path"); path != "" {
		query.Set("path", path)
	}
	if headers := mapField(transport, "headers"); headers != nil {
		if host := stringField(headers, "Host", "host"); host != "" {
			query.Set("host", host)
		}
	}
	if service := stringField(transport, "service_name"); service != "" {
		query.Set("serviceName", service)
	}
}

func applyV2RayQueryTLS(query url.Values, object map[string]any) {
	tls := mapField(object, "tls")
	if tls == nil || !boolField(tls, "enabled") {
		return
	}
	if reality := mapField(tls, "reality"); reality != nil && boolField(reality, "enabled") {
		query.Set("security", "reality")
		copyFieldFromMap(query, "pbk", reality, "public_key")
		copyFieldFromMap(query, "sid", reality, "short_id")
	} else {
		query.Set("security", "tls")
	}
	if sni := stringField(tls, "server_name"); sni != "" {
		query.Set("sni", sni)
	}
	if boolField(tls, "insecure") {
		query.Set("allowInsecure", "1")
	}
	if utls := mapField(tls, "utls"); utls != nil {
		copyFieldFromMap(query, "fp", utls, "fingerprint")
	}
}

func copyFieldFromMap(query url.Values, dst string, object map[string]any, src string) {
	if value := stringField(object, src); value != "" {
		query.Set(dst, value)
	}
}
