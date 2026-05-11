package subscription

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// supportedOutboundTypes is the set of outbound types that Resin manages.
var supportedOutboundTypes = map[string]bool{
	"socks":       true,
	"http":        true,
	"shadowsocks": true,
	"vmess":       true,
	"trojan":      true,
	"wireguard":   true,
	"hysteria":    true,
	"vless":       true,
	"shadowtls":   true,
	"tuic":        true,
	"hysteria2":   true,
	"anytls":      true,
	"tor":         true,
	"ssh":         true,
	"naive":       true,
}

// ParsedNode represents a single parsed outbound from a subscription response.
type ParsedNode struct {
	Tag        string          // original tag from the outbound config
	RawOptions json.RawMessage // full outbound JSON (including tag)
}

// subscriptionResponse is the top-level structure of a sing-box subscription.
type subscriptionResponse struct {
	Outbounds []json.RawMessage `json:"outbounds"`
}

// outboundHeader extracts just the type and tag from an outbound entry.
type outboundHeader struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type parseAttempt struct {
	nodes      []ParsedNode
	recognized bool
}

// GeneralSubscriptionParser parses common subscription formats and extracts
// sing-box outbound nodes.
type GeneralSubscriptionParser struct{}

// NewGeneralSubscriptionParser creates a general multi-format parser.
func NewGeneralSubscriptionParser() *GeneralSubscriptionParser {
	return &GeneralSubscriptionParser{}
}

// ParseGeneralSubscription parses sing-box JSON / Clash JSON|YAML / URI-line
// subscriptions (vmess/vless/trojan/ss/hysteria2/http/https/socks5/socks5h),
// plus plain HTTP proxy lines (IP:PORT or IP:PORT:USER:PASS), with optional
// base64-wrapped content support.
func ParseGeneralSubscription(data []byte) ([]ParsedNode, error) {
	return NewGeneralSubscriptionParser().Parse(data)
}

// Parse parses subscription content and returns supported outbound nodes.
func (p *GeneralSubscriptionParser) Parse(data []byte) ([]ParsedNode, error) {
	normalized := normalizeInput(data)
	if len(normalized) == 0 {
		return nil, fmt.Errorf("subscription: empty response")
	}

	attempt, err := parseSubscriptionContent(normalized)
	if err != nil {
		return nil, err
	}
	if attempt.recognized {
		return attempt.nodes, nil
	}

	if decodedText, ok := tryDecodeBase64ToText(normalized); ok {
		decodedAttempt, decodedErr := parseSubscriptionContent([]byte(decodedText))
		if decodedErr != nil {
			return nil, decodedErr
		}
		if decodedAttempt.recognized {
			return decodedAttempt.nodes, nil
		}
	}

	return nil, fmt.Errorf("subscription: unsupported format or no supported nodes found")
}

func parseSubscriptionContent(data []byte) (parseAttempt, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return parseAttempt{}, nil
	}

	if looksLikeJSON(trimmed) {
		nodes, recognized, err := parseJSONSubscription(trimmed)
		if err != nil {
			return parseAttempt{}, err
		}
		if recognized {
			return parseAttempt{nodes: nodes, recognized: true}, nil
		}
	}

	text := normalizeTextContent(string(trimmed))
	if nodes, recognized, err := parseClashYAMLSubscription(text); err != nil {
		return parseAttempt{}, err
	} else if recognized {
		return parseAttempt{nodes: nodes, recognized: true}, nil
	}

	if nodes, recognized, err := parseSurgeProxySubscription(text); err != nil {
		return parseAttempt{}, err
	} else if recognized {
		return parseAttempt{nodes: nodes, recognized: true}, nil
	}

	if nodes, recognized := parseURILineSubscription(text); recognized {
		return parseAttempt{nodes: nodes, recognized: true}, nil
	}

	return parseAttempt{}, nil
}

func parseJSONSubscription(data []byte) ([]ParsedNode, bool, error) {
	var obj map[string]json.RawMessage
	objErr := json.Unmarshal(data, &obj)
	if objErr == nil {
		if outboundsRaw, ok := obj["outbounds"]; ok {
			nodes, err := parseSingboxOutbounds(outboundsRaw)
			return nodes, true, err
		}
		if proxiesRaw, ok := obj["proxies"]; ok {
			nodes, err := parseClashProxiesJSON(proxiesRaw)
			return nodes, true, err
		}
		return nil, false, nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err == nil {
		nodes := parseRawOutbounds(arr)
		if len(nodes) == 0 {
			return nil, false, nil
		}
		return nodes, true, nil
	}

	return nil, true, fmt.Errorf("subscription: unmarshal json: %w", objErr)
}

func parseSingboxOutbounds(raw json.RawMessage) ([]ParsedNode, error) {
	var resp subscriptionResponse
	if err := json.Unmarshal(raw, &resp.Outbounds); err != nil {
		return nil, fmt.Errorf("subscription: unmarshal outbounds: %w", err)
	}
	return parseRawOutbounds(resp.Outbounds), nil
}

func parseRawOutbounds(outbounds []json.RawMessage) []ParsedNode {
	nodes := make([]ParsedNode, 0, len(outbounds))
	for _, raw := range outbounds {
		var header outboundHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			// Skip malformed individual outbound — do not fail the entire parse.
			continue
		}
		if !supportedOutboundTypes[header.Type] {
			continue
		}
		nodes = append(nodes, ParsedNode{
			Tag:        header.Tag,
			RawOptions: json.RawMessage(append([]byte(nil), raw...)),
		})
	}
	return nodes
}

func parseClashProxiesJSON(raw json.RawMessage) ([]ParsedNode, error) {
	var proxies []map[string]any
	if err := json.Unmarshal(raw, &proxies); err != nil {
		return nil, fmt.Errorf("subscription: unmarshal clash proxies: %w", err)
	}
	return parseClashProxies(proxies), nil
}

func parseClashYAMLSubscription(text string) ([]ParsedNode, bool, error) {
	if !looksLikeClashYAML(text) {
		return nil, false, nil
	}

	var cfg struct {
		Proxies    []map[string]any `yaml:"proxies"`
		ProxyUpper []map[string]any `yaml:"Proxy"`
		ProxyLower []map[string]any `yaml:"proxy"`
	}
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return nil, true, fmt.Errorf("subscription: unmarshal clash yaml: %w", err)
	}
	proxies := cfg.Proxies
	if len(proxies) == 0 && len(cfg.ProxyUpper) > 0 {
		proxies = cfg.ProxyUpper
	}
	if len(proxies) == 0 && len(cfg.ProxyLower) > 0 {
		proxies = cfg.ProxyLower
	}
	return parseClashProxies(proxies), true, nil
}

func parseClashProxies(proxies []map[string]any) []ParsedNode {
	nodes := make([]ParsedNode, 0, len(proxies))
	for _, proxy := range proxies {
		if node, ok := convertClashProxyToNode(proxy); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

type surgeProxyLine struct {
	name string
	body string
}

const surgeScannerMaxTokenSize = 1024 * 1024

func parseSurgeProxySubscription(text string) ([]ParsedNode, bool, error) {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "[proxy") && !strings.Contains(lower, "[wireguard ") {
		return nil, false, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), surgeScannerMaxTokenSize)

	var (
		nodes           []ParsedNode
		recognized      bool
		proxyLines      []surgeProxyLine
		currentSection  string
		currentWGTarget string
	)
	wireGuardSections := make(map[string]map[string]string)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			sectionTitle := strings.TrimSpace(line[1:strings.Index(line, "]")])
			sectionLower := strings.ToLower(sectionTitle)
			currentSection = ""
			currentWGTarget = ""
			switch {
			case sectionLower == "proxy":
				currentSection = "proxy"
			case strings.HasPrefix(sectionLower, "wireguard "):
				wgName := strings.TrimSpace(sectionTitle[len("WireGuard "):])
				if wgName == "" {
					wgName = strings.TrimSpace(sectionTitle[len("wireguard "):])
				}
				wgName = strings.ToLower(strings.TrimSpace(wgName))
				if wgName != "" {
					currentSection = "wireguard"
					currentWGTarget = wgName
					if _, exists := wireGuardSections[wgName]; !exists {
						wireGuardSections[wgName] = make(map[string]string)
					}
				}
			}
			continue
		}

		switch currentSection {
		case "proxy":
			name, body, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			body = strings.TrimSpace(body)
			if name == "" || body == "" {
				continue
			}
			proxyLines = append(proxyLines, surgeProxyLine{name: name, body: body})
		case "wireguard":
			if currentWGTarget == "" {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)
			if key == "" || value == "" {
				continue
			}
			wireGuardSections[currentWGTarget][key] = value
		}
	}

	for _, item := range proxyLines {
		node, ok, seen := parseSurgeProxyLine(item.name, item.body, wireGuardSections)
		if seen {
			recognized = true
		}
		if ok {
			nodes = append(nodes, node)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, true, fmt.Errorf("subscription: scan surge proxy: %w", err)
	}
	return nodes, recognized, nil
}

func parseSurgeProxyLine(name string, body string, wireGuardSections map[string]map[string]string) (ParsedNode, bool, bool) {
	parts := splitCommaRespectQuotes(body)
	if len(parts) == 0 {
		return ParsedNode{}, false, false
	}
	proto := strings.ToLower(strings.TrimSpace(parts[0]))
	if strings.EqualFold(proto, "custom") {
		proxy, ok := parseSurgeCustomProxyLine(name, parts)
		if !ok {
			return ParsedNode{}, false, true
		}
		node, ok := convertClashProxyToNode(proxy)
		return node, ok, true
	}
	if proxy, ok := parseSurgeQXProxyLine(name, parts); ok {
		node, ok := convertClashProxyToNode(proxy)
		return node, ok, true
	}
	switch proto {
	case "ss", "shadowsocks", "vmess", "vmess-aead", "trojan", "socks5", "http", "https",
		"vless", "wireguard", "wg", "hysteria", "hysteria2", "hy2", "tuic", "ssh":
		// supported and convertible to sing-box compatible outbounds
	case "ssr", "snell", "shadowtls", "naive":
		// Keep unsupported Surge node types as "unrecognized" to avoid silent drops.
		return ParsedNode{}, false, false
	default:
		return ParsedNode{}, false, false
	}
	if proto == "wireguard" || proto == "wg" {
		sectionOptions := parseSurgeOptions(parts[1:])
		if sectionName := strings.TrimSpace(surgeOption(sectionOptions, "section-name", "section_name")); sectionName != "" {
			proxy, ok := buildSurgeWireGuardProxyFromSection(name, sectionName, sectionOptions, wireGuardSections)
			if !ok {
				return ParsedNode{}, false, true
			}
			node, ok := convertClashProxyToNode(proxy)
			return node, ok, true
		}
	}
	if len(parts) < 3 {
		return ParsedNode{}, false, true
	}

	server := strings.TrimSpace(parts[1])
	portText := strings.TrimSpace(parts[2])
	if server == "" || portText == "" {
		return ParsedNode{}, false, true
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return ParsedNode{}, false, true
	}

	options := parseSurgeOptions(parts[3:])
	proxy := map[string]any{
		"name":   name,
		"server": server,
		"port":   port,
	}
	if udpRelay, ok := parseSurgeBool(surgeOption(options, "udp-relay", "udp")); ok {
		proxy["udp"] = udpRelay
	}
	if tfo, ok := parseSurgeBool(surgeOption(options, "tfo", "fast-open")); ok {
		proxy["fast-open"] = tfo
	}
	if skipVerify, ok := parseSurgeBool(surgeOption(options, "skip-cert-verify")); ok {
		proxy["skip-cert-verify"] = skipVerify
	}

	switch proto {
	case "ss", "shadowsocks":
		method := strings.TrimSpace(surgeOption(options, "encrypt-method", "method", "cipher"))
		password := strings.TrimSpace(surgeOption(options, "password"))
		if method == "" || password == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "ss"
		proxy["cipher"] = method
		proxy["password"] = password
		if obfs := strings.TrimSpace(surgeOption(options, "obfs")); obfs != "" {
			proxy["obfs"] = obfs
		}
		if obfsHost := strings.TrimSpace(surgeOption(options, "obfs-host")); obfsHost != "" {
			proxy["obfs-host"] = obfsHost
		}
		if plugin := strings.TrimSpace(surgeOption(options, "plugin")); plugin != "" {
			proxy["plugin"] = plugin
		}
		if pluginOpts := strings.TrimSpace(surgeOption(options, "plugin-opts", "plugin_opts", "plugin-option")); pluginOpts != "" {
			proxy["plugin-opts"] = pluginOpts
		}
	case "vmess", "vmess-aead":
		uuid := strings.TrimSpace(surgeOption(options, "username", "uuid", "id"))
		if uuid == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "vmess"
		proxy["uuid"] = uuid
		if method := strings.TrimSpace(surgeOption(options, "encrypt-method", "method", "cipher")); method != "" {
			proxy["cipher"] = method
		}
		if alterID := strings.TrimSpace(surgeOption(options, "alterid", "alter-id", "alter_id")); alterID != "" {
			proxy["alterId"] = alterID
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if tls, ok := parseSurgeBool(surgeOption(options, "tls")); ok {
			proxy["tls"] = tls
		}
		applySurgeV2RayNetworkOptions(proxy, options)
	case "trojan":
		password := strings.TrimSpace(surgeOption(options, "password"))
		if password == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "trojan"
		proxy["password"] = password
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if tls, ok := parseSurgeBool(surgeOption(options, "tls")); ok {
			proxy["tls"] = tls
		}
		applySurgeV2RayNetworkOptions(proxy, options)
	case "vless":
		uuid := strings.TrimSpace(surgeOption(options, "username", "uuid", "id"))
		if uuid == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "vless"
		proxy["uuid"] = uuid
		if flow := strings.TrimSpace(surgeOption(options, "flow")); flow != "" {
			proxy["flow"] = flow
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if tls, ok := parseSurgeBool(surgeOption(options, "tls")); ok {
			proxy["tls"] = tls
		}
		applySurgeV2RayNetworkOptions(proxy, options)
	case "socks5":
		proxy["type"] = "socks5"
		if username := strings.TrimSpace(surgeOption(options, "username")); username != "" {
			proxy["username"] = username
		}
		if password := strings.TrimSpace(surgeOption(options, "password")); password != "" {
			proxy["password"] = password
		}
	case "http", "https":
		proxy["type"] = "http"
		if proto == "https" {
			proxy["tls"] = true
		}
		if tls, ok := parseSurgeBool(surgeOption(options, "tls")); ok {
			proxy["tls"] = tls
		}
		if username := strings.TrimSpace(surgeOption(options, "username")); username != "" {
			proxy["username"] = username
		}
		if password := strings.TrimSpace(surgeOption(options, "password")); password != "" {
			proxy["password"] = password
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
	case "wireguard", "wg":
		privateKey := strings.TrimSpace(surgeOption(options, "private-key", "private_key"))
		publicKey := strings.TrimSpace(surgeOption(options, "public-key", "public_key", "peer-public-key", "peer_public_key"))
		if privateKey == "" || publicKey == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "wireguard"
		proxy["private-key"] = privateKey
		proxy["public-key"] = publicKey
		if preSharedKey := strings.TrimSpace(surgeOption(options, "pre-shared-key", "pre_shared_key", "preshared-key", "preshared_key")); preSharedKey != "" {
			proxy["pre-shared-key"] = preSharedKey
		}
		if ip := strings.TrimSpace(surgeOption(options, "ip", "self-ip", "self_ip", "self-ipv4", "self_ipv4")); ip != "" {
			proxy["ip"] = ip
		}
		if ipv6 := strings.TrimSpace(surgeOption(options, "ipv6", "self-ip-v6", "self_ip_v6", "self-ipv6", "self_ipv6")); ipv6 != "" {
			proxy["ipv6"] = ipv6
		}
		if allowedIPs := strings.TrimSpace(surgeOption(options, "allowed-ips", "allowed_ips")); allowedIPs != "" {
			proxy["allowed-ips"] = allowedIPs
		}
		if reserved := parseSurgeReservedBytes(surgeOption(options, "reserved")); len(reserved) == 3 {
			proxy["reserved"] = reserved
		}
		if mtu := strings.TrimSpace(surgeOption(options, "mtu")); mtu != "" {
			proxy["mtu"] = mtu
		}
	case "hysteria":
		proxy["type"] = "hysteria"
		if auth := strings.TrimSpace(surgeOption(options, "auth-str", "auth_str", "auth")); auth != "" {
			proxy["auth-str"] = auth
		}
		if up := strings.TrimSpace(surgeOption(options, "up", "up-speed", "up_speed")); up != "" {
			proxy["up"] = up
		}
		if down := strings.TrimSpace(surgeOption(options, "down", "down-speed", "down_speed")); down != "" {
			proxy["down"] = down
		}
		if ports := strings.TrimSpace(surgeOption(options, "ports")); ports != "" {
			proxy["ports"] = ports
		}
		if protocol := strings.TrimSpace(surgeOption(options, "protocol")); protocol != "" {
			proxy["protocol"] = protocol
		}
		if obfs := strings.TrimSpace(surgeOption(options, "obfs")); obfs != "" {
			proxy["obfs"] = obfs
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if fingerprint := strings.TrimSpace(surgeOption(options, "fingerprint", "client-fingerprint", "client_fingerprint", "fp")); fingerprint != "" {
			proxy["fingerprint"] = fingerprint
		}
		if alpn := strings.TrimSpace(surgeOption(options, "alpn")); alpn != "" {
			proxy["alpn"] = alpn
		}
		if ca := strings.TrimSpace(surgeOption(options, "ca")); ca != "" {
			proxy["ca"] = ca
		}
		if caStr := strings.TrimSpace(surgeOption(options, "ca-str", "ca_str")); caStr != "" {
			proxy["ca-str"] = caStr
		}
		if recvWindowConn := strings.TrimSpace(surgeOption(options, "recv-window-conn", "recv_window_conn")); recvWindowConn != "" {
			proxy["recv-window-conn"] = recvWindowConn
		}
		if recvWindow := strings.TrimSpace(surgeOption(options, "recv-window", "recv_window")); recvWindow != "" {
			proxy["recv-window"] = recvWindow
		}
		if disableMTUDiscovery, ok := parseSurgeBool(surgeOption(options, "disable-mtu-discovery", "disable_mtu_discovery")); ok {
			proxy["disable_mtu_discovery"] = disableMTUDiscovery
		}
		if hopInterval := strings.TrimSpace(surgeOption(options, "hop-interval", "hop_interval")); hopInterval != "" {
			proxy["hop-interval"] = hopInterval
		}
	case "hysteria2", "hy2":
		password := strings.TrimSpace(surgeOption(options, "password", "auth"))
		if password == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "hysteria2"
		proxy["password"] = password
		if ports := strings.TrimSpace(surgeOption(options, "ports")); ports != "" {
			proxy["ports"] = ports
		}
		if up := strings.TrimSpace(surgeOption(options, "up", "up-mbps", "up_mbps")); up != "" {
			proxy["up"] = up
		}
		if down := strings.TrimSpace(surgeOption(options, "down", "down-mbps", "down_mbps")); down != "" {
			proxy["down"] = down
		}
		if obfs := strings.TrimSpace(surgeOption(options, "obfs")); obfs != "" {
			proxy["obfs"] = obfs
		}
		if obfsPassword := strings.TrimSpace(surgeOption(options, "obfs-password", "obfs_password")); obfsPassword != "" {
			proxy["obfs-password"] = obfsPassword
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if fingerprint := strings.TrimSpace(surgeOption(options, "fingerprint", "client-fingerprint", "client_fingerprint", "fp")); fingerprint != "" {
			proxy["fingerprint"] = fingerprint
		}
		if alpn := strings.TrimSpace(surgeOption(options, "alpn")); alpn != "" {
			proxy["alpn"] = alpn
		}
		if ca := strings.TrimSpace(surgeOption(options, "ca")); ca != "" {
			proxy["ca"] = ca
		}
		if caStr := strings.TrimSpace(surgeOption(options, "ca-str", "ca_str")); caStr != "" {
			proxy["ca-str"] = caStr
		}
		if hopInterval := strings.TrimSpace(surgeOption(options, "hop-interval", "hop_interval")); hopInterval != "" {
			proxy["hop-interval"] = hopInterval
		}
	case "tuic":
		uuid := strings.TrimSpace(surgeOption(options, "uuid", "username"))
		if uuid == "" {
			return ParsedNode{}, false, true
		}
		proxy["type"] = "tuic"
		proxy["uuid"] = uuid
		if password := strings.TrimSpace(surgeOption(options, "password")); password != "" {
			proxy["password"] = password
		}
		if sni := strings.TrimSpace(surgeOption(options, "sni", "servername", "peer")); sni != "" {
			proxy["sni"] = sni
		}
		if congestion := strings.TrimSpace(surgeOption(options, "congestion-controller", "congestion_control")); congestion != "" {
			proxy["congestion-controller"] = congestion
		}
		if udpRelayMode := strings.TrimSpace(surgeOption(options, "udp-relay-mode", "udp_relay_mode")); udpRelayMode != "" {
			proxy["udp-relay-mode"] = udpRelayMode
		}
		if reduceRTT, ok := parseSurgeBool(surgeOption(options, "reduce-rtt", "zero-rtt-handshake", "zero_rtt_handshake")); ok {
			proxy["reduce-rtt"] = reduceRTT
		}
		if heartbeat := strings.TrimSpace(surgeOption(options, "heartbeat-interval", "heartbeat_interval", "heartbeat")); heartbeat != "" {
			proxy["heartbeat-interval"] = heartbeat
		}
		if disableSNI, ok := parseSurgeBool(surgeOption(options, "disable-sni", "disable_sni")); ok {
			proxy["disable-sni"] = disableSNI
		}
		if alpn := strings.TrimSpace(surgeOption(options, "alpn")); alpn != "" {
			proxy["alpn"] = alpn
		}
	case "ssh":
		proxy["type"] = "ssh"
		if user := strings.TrimSpace(surgeOption(options, "user", "username")); user != "" {
			proxy["user"] = user
		}
		if password := strings.TrimSpace(surgeOption(options, "password")); password != "" {
			proxy["password"] = password
		}
		if privateKey := strings.TrimSpace(surgeOption(options, "private-key", "private_key")); privateKey != "" {
			proxy["private-key"] = privateKey
		}
		if passphrase := strings.TrimSpace(surgeOption(options, "private-key-passphrase", "private_key_passphrase")); passphrase != "" {
			proxy["private-key-passphrase"] = passphrase
		}
		if hostKey := strings.TrimSpace(surgeOption(options, "host-key", "host_key")); hostKey != "" {
			proxy["host-key"] = hostKey
		}
		if hostKeyAlgorithms := strings.TrimSpace(surgeOption(options, "host-key-algorithms", "host_key_algorithms")); hostKeyAlgorithms != "" {
			proxy["host-key-algorithms"] = hostKeyAlgorithms
		}
		if clientVersion := strings.TrimSpace(surgeOption(options, "client-version", "client_version")); clientVersion != "" {
			proxy["client-version"] = clientVersion
		}
	}

	node, ok := convertClashProxyToNode(proxy)
	return node, ok, true
}

func parseSurgeCustomProxyLine(name string, parts []string) (map[string]any, bool) {
	if len(parts) < 5 {
		return nil, false
	}
	server := strings.TrimSpace(parts[1])
	port, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 16)
	if err != nil || port == 0 || server == "" {
		return nil, false
	}
	method := strings.TrimSpace(parts[3])
	password := strings.TrimSpace(parts[4])
	if method == "" || password == "" {
		return nil, false
	}

	proxy := map[string]any{
		"type":     "ss",
		"name":     name,
		"server":   server,
		"port":     port,
		"cipher":   method,
		"password": password,
	}
	options := parseSurgeOptions(parts[5:])
	if obfs := strings.TrimSpace(surgeOption(options, "obfs")); obfs != "" {
		proxy["obfs"] = obfs
	}
	if obfsHost := strings.TrimSpace(surgeOption(options, "obfs-host")); obfsHost != "" {
		proxy["obfs-host"] = obfsHost
	}
	if udpRelay, ok := parseSurgeBool(surgeOption(options, "udp-relay", "udp")); ok {
		proxy["udp"] = udpRelay
	}
	if tfo, ok := parseSurgeBool(surgeOption(options, "tfo", "fast-open")); ok {
		proxy["fast-open"] = tfo
	}
	if skipVerify, ok := parseSurgeBool(surgeOption(options, "skip-cert-verify")); ok {
		proxy["skip-cert-verify"] = skipVerify
	}
	return proxy, true
}

func parseSurgeQXProxyLine(name string, parts []string) (map[string]any, bool) {
	qxType := strings.ToLower(strings.TrimSpace(name))
	switch qxType {
	case "shadowsocks", "vmess", "trojan", "http":
	default:
		return nil, false
	}
	if len(parts) < 2 {
		return nil, false
	}
	server, port, ok := parseHostPort(strings.TrimSpace(parts[0]))
	if !ok {
		return nil, false
	}
	options := parseSurgeOptions(parts[1:])
	tag := strings.TrimSpace(surgeOption(options, "tag", "remarks", "name"))
	if tag == "" {
		tag = name
	}
	proxy := map[string]any{
		"name":   tag,
		"server": server,
		"port":   port,
	}
	if udpRelay, ok := parseSurgeBool(surgeOption(options, "udp-relay", "udp")); ok {
		proxy["udp"] = udpRelay
	}
	if tfo, ok := parseSurgeBool(surgeOption(options, "fast-open", "tfo")); ok {
		proxy["fast-open"] = tfo
	}
	if verify, ok := parseSurgeBool(surgeOption(options, "tls-verification")); ok {
		proxy["skip-cert-verify"] = !verify
	}
	if skipVerify, ok := parseSurgeBool(surgeOption(options, "skip-cert-verify")); ok {
		proxy["skip-cert-verify"] = skipVerify
	}

	switch qxType {
	case "shadowsocks":
		method := strings.TrimSpace(surgeOption(options, "method", "encrypt-method", "cipher"))
		password := strings.TrimSpace(surgeOption(options, "password"))
		if method == "" || password == "" {
			return nil, false
		}
		proxy["type"] = "ss"
		proxy["cipher"] = method
		proxy["password"] = password

		obfsMode := strings.ToLower(strings.TrimSpace(surgeOption(options, "obfs")))
		obfsHost := strings.TrimSpace(surgeOption(options, "obfs-host"))
		path := strings.TrimSpace(surgeOption(options, "obfs-uri"))
		switch obfsMode {
		case "http", "tls":
			proxy["obfs"] = obfsMode
			if obfsHost != "" {
				proxy["obfs-host"] = obfsHost
			}
		case "ws", "wss":
			proxy["plugin"] = "v2ray-plugin"
			pluginParts := []string{"mode=websocket"}
			if obfsHost != "" {
				pluginParts = append(pluginParts, "host="+obfsHost)
			}
			if path != "" {
				pluginParts = append(pluginParts, "path="+path)
			}
			if obfsMode == "wss" {
				pluginParts = append(pluginParts, "tls")
			}
			proxy["plugin-opts"] = strings.Join(pluginParts, ";")
		}
	case "vmess":
		uuid := strings.TrimSpace(surgeOption(options, "password", "id", "uuid"))
		if uuid == "" {
			return nil, false
		}
		proxy["type"] = "vmess"
		proxy["uuid"] = uuid
		if method := strings.TrimSpace(surgeOption(options, "method", "cipher", "security")); method != "" {
			proxy["cipher"] = method
		}
		obfsMode := strings.ToLower(strings.TrimSpace(surgeOption(options, "obfs")))
		host := strings.TrimSpace(surgeOption(options, "obfs-host", "tls-host"))
		path := strings.TrimSpace(surgeOption(options, "obfs-uri"))
		tls := false
		if overTLS, ok := parseSurgeBool(surgeOption(options, "over-tls")); ok && overTLS {
			tls = true
		}
		if obfsMode == "wss" {
			tls = true
		}
		switch obfsMode {
		case "ws", "wss":
			proxy["network"] = "ws"
			wsOpts := map[string]any{}
			if path != "" {
				wsOpts["path"] = path
			}
			if host != "" {
				wsOpts["headers"] = map[string]any{"Host": host}
			}
			if len(wsOpts) > 0 {
				proxy["ws-opts"] = wsOpts
			}
		}
		if tls {
			proxy["tls"] = true
		}
	case "trojan":
		password := strings.TrimSpace(surgeOption(options, "password"))
		if password == "" {
			return nil, false
		}
		proxy["type"] = "trojan"
		proxy["password"] = password
		if host := strings.TrimSpace(surgeOption(options, "tls-host", "sni")); host != "" {
			proxy["sni"] = host
		}
		if overTLS, ok := parseSurgeBool(surgeOption(options, "over-tls")); ok {
			proxy["tls"] = overTLS
		} else {
			proxy["tls"] = true
		}
	case "http":
		proxy["type"] = "http"
		if username := strings.TrimSpace(surgeOption(options, "username")); username != "" {
			proxy["username"] = username
		}
		if password := strings.TrimSpace(surgeOption(options, "password")); password != "" {
			proxy["password"] = password
		}
		if overTLS, ok := parseSurgeBool(surgeOption(options, "over-tls")); ok {
			proxy["tls"] = overTLS
		}
	}

	return proxy, true
}

func buildSurgeWireGuardProxyFromSection(name string, sectionName string, lineOptions map[string]string, wireGuardSections map[string]map[string]string) (map[string]any, bool) {
	section := wireGuardSections[strings.ToLower(strings.TrimSpace(sectionName))]
	if len(section) == 0 {
		return nil, false
	}
	privateKey := strings.TrimSpace(firstNonEmpty(
		section["private-key"],
		section["private_key"],
	))
	if privateKey == "" {
		return nil, false
	}
	ip := strings.TrimSpace(firstNonEmpty(
		section["self-ip"],
		section["self_ip"],
		section["ip"],
	))
	ipv6 := strings.TrimSpace(firstNonEmpty(
		section["self-ip-v6"],
		section["self_ip_v6"],
		section["self-ipv6"],
		section["ipv6"],
	))

	peer := strings.TrimSpace(section["peer"])
	server, port, publicKey, preSharedKey, allowedIPs, ok := parseSurgeWireGuardPeer(peer)
	if !ok {
		return nil, false
	}

	proxy := map[string]any{
		"type":        "wireguard",
		"name":        name,
		"server":      server,
		"port":        port,
		"private-key": privateKey,
		"public-key":  publicKey,
	}
	if ip != "" {
		proxy["ip"] = ip
	}
	if ipv6 != "" {
		proxy["ipv6"] = ipv6
	}
	if allowedIPs != "" {
		proxy["allowed-ips"] = allowedIPs
	}
	if preSharedKey != "" {
		proxy["pre-shared-key"] = preSharedKey
	}
	if mtu := strings.TrimSpace(section["mtu"]); mtu != "" {
		proxy["mtu"] = mtu
	}
	if udpRelay, ok := parseSurgeBool(surgeOption(lineOptions, "udp-relay", "udp")); ok {
		proxy["udp"] = udpRelay
	}
	if tfo, ok := parseSurgeBool(surgeOption(lineOptions, "tfo", "fast-open")); ok {
		proxy["fast-open"] = tfo
	}
	if skipVerify, ok := parseSurgeBool(surgeOption(lineOptions, "skip-cert-verify")); ok {
		proxy["skip-cert-verify"] = skipVerify
	}
	return proxy, true
}

func parseSurgeWireGuardPeer(raw string) (string, uint64, string, string, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, "", "", "", false
	}
	if start := strings.Index(raw, "("); start >= 0 {
		segment := raw[start+1:]
		if end := strings.Index(segment, ")"); end >= 0 {
			raw = segment[:end]
		}
	}
	parts := splitCommaRespectQuotes(raw)
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key != "" && value != "" {
			values[key] = value
		}
	}
	endpoint := strings.TrimSpace(firstNonEmpty(values["endpoint"], values["address"]))
	publicKey := strings.TrimSpace(firstNonEmpty(values["public-key"], values["public_key"]))
	if endpoint == "" || publicKey == "" {
		return "", 0, "", "", "", false
	}
	server, port, ok := parseHostPort(endpoint)
	if !ok {
		return "", 0, "", "", "", false
	}
	preSharedKey := strings.TrimSpace(firstNonEmpty(
		values["pre-shared-key"],
		values["pre_shared_key"],
		values["preshared-key"],
		values["preshared_key"],
	))
	allowedIPs := strings.TrimSpace(firstNonEmpty(values["allowed-ips"], values["allowed_ips"]))
	return server, port, publicKey, preSharedKey, allowedIPs, true
}

func applySurgeV2RayNetworkOptions(proxy map[string]any, options map[string]string) {
	network := normalizeV2RayNetwork(surgeOption(options, "network"))
	if ws, ok := parseSurgeBool(surgeOption(options, "ws")); ok && ws {
		network = "ws"
	}
	if grpc, ok := parseSurgeBool(surgeOption(options, "grpc")); ok && grpc {
		network = "grpc"
	}
	if h2, ok := parseSurgeBool(surgeOption(options, "h2", "http2")); ok && h2 {
		network = "h2"
	}
	if quic, ok := parseSurgeBool(surgeOption(options, "quic")); ok && quic {
		network = "quic"
	}
	if network == "" {
		return
	}
	proxy["network"] = network

	switch network {
	case "ws":
		wsOpts := map[string]any{}
		if path := strings.TrimSpace(surgeOption(options, "ws-path", "path", "wspath")); path != "" {
			wsOpts["path"] = path
		}
		if headers := parseSurgeHeaderString(surgeOption(options, "ws-headers", "ws_headers")); len(headers) > 0 {
			wsOpts["headers"] = headers
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
	case "grpc":
		if serviceName := strings.TrimSpace(surgeOption(options, "grpc-service-name", "service-name", "service_name")); serviceName != "" {
			proxy["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "h2", "http":
		httpOpts := map[string]any{}
		if path := strings.TrimSpace(surgeOption(options, "h2-path", "http-path", "path")); path != "" {
			httpOpts["path"] = path
		}
		if host := strings.TrimSpace(surgeOption(options, "h2-host", "http-host", "host")); host != "" {
			httpOpts["host"] = splitCommaList(host)
		}
		if len(httpOpts) > 0 {
			if network == "h2" {
				proxy["h2-opts"] = httpOpts
			} else {
				proxy["http-opts"] = httpOpts
			}
		}
	case "quic":
		// QUIC transport has no extra user-facing options here.
	case "httpupgrade", "http-upgrade":
		httpUpgradeOpts := map[string]any{}
		if path := strings.TrimSpace(surgeOption(options, "httpupgrade-path", "path")); path != "" {
			httpUpgradeOpts["path"] = path
		}
		if host := strings.TrimSpace(surgeOption(options, "httpupgrade-host", "host")); host != "" {
			httpUpgradeOpts["host"] = host
		}
		if len(httpUpgradeOpts) > 0 {
			proxy["http-upgrade-opts"] = httpUpgradeOpts
		}
	}
}

func parseSurgeOptions(parts []string) map[string]string {
	options := make(map[string]string, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			options[strings.ToLower(part)] = "true"
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		options[key] = value
	}
	return options
}

func surgeOption(options map[string]string, keys ...string) string {
	for _, key := range keys {
		if value, ok := options[strings.ToLower(strings.TrimSpace(key))]; ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value
			}
		}
	}
	return ""
}

func parseSurgeBool(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parseSurgeHeaderString(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '|' || r == ';'
	})
	headers := make(map[string]any, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			key, value, ok = strings.Cut(part, "=")
		}
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		headers[key] = value
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func parseSurgeReservedBytes(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '/' || r == ':' || r == '|' || r == ' '
	})
	if len(parts) != 3 {
		return nil
	}
	values := make([]int, 0, 3)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 || v > 255 {
			return nil
		}
		values = append(values, v)
	}
	return values
}

func splitCommaRespectQuotes(input string) []string {
	var (
		out   []string
		token strings.Builder
		quote rune
	)
	for _, r := range input {
		switch r {
		case '"', '\'':
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
			token.WriteRune(r)
		case ',':
			if quote != 0 {
				token.WriteRune(r)
				continue
			}
			out = append(out, strings.TrimSpace(token.String()))
			token.Reset()
		default:
			token.WriteRune(r)
		}
	}
	out = append(out, strings.TrimSpace(token.String()))
	return out
}

func convertClashProxyToNode(proxy map[string]any) (ParsedNode, bool) {
	nodeType := strings.ToLower(strings.TrimSpace(getString(proxy, "type")))
	tag := strings.TrimSpace(firstNonEmpty(getString(proxy, "name"), getString(proxy, "tag")))
	server := strings.TrimSpace(getString(proxy, "server"))
	port, ok := getUint(proxy, "port")
	if !ok || server == "" {
		return ParsedNode{}, false
	}

	switch nodeType {
	case "ss", "shadowsocks":
		method := normalizeShadowsocksMethod(firstNonEmpty(getString(proxy, "cipher"), getString(proxy, "method")))
		password := strings.TrimSpace(getString(proxy, "password"))
		if method == "" || password == "" {
			return ParsedNode{}, false
		}
		outbound := map[string]any{
			"type":        "shadowsocks",
			"tag":         defaultTag(tag, "shadowsocks", server, port),
			"server":      server,
			"server_port": port,
			"method":      method,
			"password":    password,
		}
		setSSPluginFromClash(outbound, proxy)
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "vmess":
		uuid := strings.TrimSpace(getString(proxy, "uuid"))
		if uuid == "" {
			return ParsedNode{}, false
		}
		security := strings.TrimSpace(firstNonEmpty(getString(proxy, "cipher"), getString(proxy, "security")))
		if security == "" {
			security = "auto"
		}
		outbound := map[string]any{
			"type":        "vmess",
			"tag":         defaultTag(tag, "vmess", server, port),
			"server":      server,
			"server_port": port,
			"uuid":        uuid,
			"security":    security,
		}
		if alterID, ok := getUint(proxy, "alterId", "alter_id", "aid"); ok {
			outbound["alter_id"] = alterID
		} else {
			outbound["alter_id"] = uint64(0)
		}
		setTLSFromClash(outbound, proxy, "tls")
		if !setV2RayTransportFromClash(outbound, proxy) {
			return ParsedNode{}, false
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "vless":
		uuid := strings.TrimSpace(getString(proxy, "uuid"))
		if uuid == "" {
			return ParsedNode{}, false
		}
		outbound := map[string]any{
			"type":        "vless",
			"tag":         defaultTag(tag, "vless", server, port),
			"server":      server,
			"server_port": port,
			"uuid":        uuid,
		}
		if flow := normalizeVLESSFlow(getString(proxy, "flow")); flow != "" {
			outbound["flow"] = flow
		}
		setTLSFromClash(outbound, proxy, "tls")
		if !setV2RayTransportFromClash(outbound, proxy) {
			return ParsedNode{}, false
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "trojan":
		password := strings.TrimSpace(getString(proxy, "password"))
		if password == "" {
			return ParsedNode{}, false
		}
		tlsEnabled := true
		if v, ok := getBool(proxy, "tls"); ok {
			tlsEnabled = v
		}
		serverName := firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "servername"),
			getString(proxy, "peer"),
		)
		tls := map[string]any{
			"enabled":     tlsEnabled,
			"server_name": firstNonEmpty(strings.TrimSpace(serverName), server),
		}
		if insecure, ok := getBool(proxy, "skip-cert-verify", "allowInsecure", "insecure"); ok && insecure {
			tls["insecure"] = true
		}
		outbound := map[string]any{
			"type":        "trojan",
			"tag":         defaultTag(tag, "trojan", server, port),
			"server":      server,
			"server_port": port,
			"password":    password,
			"tls":         tls,
		}
		if !setV2RayTransportFromClash(outbound, proxy) {
			return ParsedNode{}, false
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "hysteria2", "hy2":
		password := strings.TrimSpace(firstNonEmpty(getString(proxy, "password"), getString(proxy, "auth")))
		if password == "" {
			return ParsedNode{}, false
		}
		serverName := firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "peer"),
			getString(proxy, "servername"),
		)
		tls := map[string]any{
			"enabled":     true,
			"server_name": firstNonEmpty(strings.TrimSpace(serverName), server),
		}
		if insecure, ok := getBool(proxy, "skip-cert-verify", "insecure", "allowInsecure"); ok && insecure {
			tls["insecure"] = true
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		applyUTLSFromValue(tls, firstNonEmpty(
			getString(proxy, "fingerprint"),
			getString(proxy, "client-fingerprint"),
			getString(proxy, "client_fingerprint"),
			getString(proxy, "fp"),
		))
		applyTLSCertificateFromClash(tls, proxy)
		outbound := map[string]any{
			"type":        "hysteria2",
			"tag":         defaultTag(tag, "hysteria2", server, port),
			"server":      server,
			"server_port": port,
			"password":    password,
			"tls":         tls,
		}
		if ports := normalizeHysteriaPortList(firstNonEmpty(getString(proxy, "ports"), getString(proxy, "mport"))); len(ports) > 0 {
			outbound["server_ports"] = ports
		}
		if upMbps, ok := getUint(proxy, "up", "up-mbps", "up_mbps"); ok {
			outbound["up_mbps"] = upMbps
		}
		if downMbps, ok := getUint(proxy, "down", "down-mbps", "down_mbps"); ok {
			outbound["down_mbps"] = downMbps
		}
		if hopInterval, ok := getDurationString(proxy, "s", "hop-interval", "hop_interval"); ok {
			outbound["hop_interval"] = hopInterval
		}
		if obfsType := strings.TrimSpace(getString(proxy, "obfs")); obfsType != "" {
			obfs := map[string]any{"type": obfsType}
			if obfsPassword := strings.TrimSpace(firstNonEmpty(
				getString(proxy, "obfs-password"),
				getString(proxy, "obfs_password"),
			)); obfsPassword != "" {
				obfs["password"] = obfsPassword
			}
			outbound["obfs"] = obfs
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "socks", "socks4", "socks4a", "socks5":
		outbound := map[string]any{
			"type":        "socks",
			"tag":         defaultTag(tag, "socks", server, port),
			"server":      server,
			"server_port": port,
		}
		if version := clashSOCKSVersion(nodeType, proxy); version != "" {
			outbound["version"] = version
		}
		if username := strings.TrimSpace(getString(proxy, "username")); username != "" {
			outbound["username"] = username
		}
		if password := strings.TrimSpace(getString(proxy, "password")); password != "" {
			outbound["password"] = password
		}
		if udp, ok := getBool(proxy, "udp"); ok && !udp {
			outbound["network"] = "tcp"
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "http":
		outbound := map[string]any{
			"type":        "http",
			"tag":         defaultTag(tag, "http", server, port),
			"server":      server,
			"server_port": port,
		}
		if username := strings.TrimSpace(getString(proxy, "username")); username != "" {
			outbound["username"] = username
		}
		if password := strings.TrimSpace(getString(proxy, "password")); password != "" {
			outbound["password"] = password
		}
		if headers, ok := getMap(proxy, "headers"); ok && len(headers) > 0 {
			outbound["headers"] = headers
		}
		sni := strings.TrimSpace(firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "servername"),
			getString(proxy, "server-name"),
		))
		skipVerify, hasSkipVerify := getBool(proxy, "skip-cert-verify", "allowInsecure", "insecure")
		tlsEnabled := false
		if tls, ok := getBool(proxy, "tls"); ok && tls {
			tlsEnabled = true
		}
		if sni != "" || hasSkipVerify {
			tlsEnabled = true
		}
		if tlsEnabled {
			tls := newClashEnabledTLS(sni, hasSkipVerify && skipVerify, nil)
			outbound["tls"] = tls
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "wireguard", "wg":
		privateKey := strings.TrimSpace(getString(proxy, "private-key", "private_key"))
		publicKey := strings.TrimSpace(getString(proxy, "public-key", "public_key"))
		localAddress := parseWireGuardLocalAddress(proxy)
		allowedIPs := parseWireGuardAllowedIPs(proxy)
		if len(allowedIPs) == 0 {
			allowedIPs = []string{"0.0.0.0/0", "::/0"}
		}
		if privateKey == "" || publicKey == "" || len(localAddress) == 0 {
			return ParsedNode{}, false
		}
		outbound := map[string]any{
			"type":            "wireguard",
			"tag":             defaultTag(tag, "wireguard", server, port),
			"server":          server,
			"server_port":     port,
			"private_key":     privateKey,
			"peer_public_key": publicKey,
			"local_address":   localAddress,
		}
		peer := map[string]any{
			"server":      server,
			"server_port": port,
			"public_key":  publicKey,
			"allowed_ips": allowedIPs,
		}
		if preSharedKey := strings.TrimSpace(getString(proxy, "pre-shared-key", "pre_shared_key")); preSharedKey != "" {
			outbound["pre_shared_key"] = preSharedKey
			peer["pre_shared_key"] = preSharedKey
		}
		if reserved, ok := getUint8Array(proxy, "reserved"); ok && len(reserved) == 3 {
			outbound["reserved"] = reserved
			peer["reserved"] = reserved
		}
		outbound["peers"] = []map[string]any{peer}
		if mtu, ok := getUint(proxy, "mtu"); ok {
			outbound["mtu"] = mtu
		}
		if udp, ok := getBool(proxy, "udp"); ok && !udp {
			outbound["network"] = "tcp"
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "hysteria":
		authString := strings.TrimSpace(firstNonEmpty(
			getString(proxy, "auth-str", "auth_str"),
			getString(proxy, "auth"),
		))
		up := normalizeHysteriaRate(firstNonEmpty(
			getString(proxy, "up"),
			getString(proxy, "up-speed"),
			getString(proxy, "up_speed"),
		))
		down := normalizeHysteriaRate(firstNonEmpty(
			getString(proxy, "down"),
			getString(proxy, "down-speed"),
			getString(proxy, "down_speed"),
		))
		sni := strings.TrimSpace(firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "servername"),
			getString(proxy, "server-name"),
		))
		insecure, _ := getBool(proxy, "skip-cert-verify", "allowInsecure", "insecure")
		tls := newClashEnabledTLS(sni, insecure, getStringSlice(proxy, "alpn"))
		applyUTLSFromValue(tls, firstNonEmpty(
			getString(proxy, "fingerprint"),
			getString(proxy, "client-fingerprint"),
			getString(proxy, "client_fingerprint"),
			getString(proxy, "fp"),
		))
		applyTLSCertificateFromClash(tls, proxy)
		outbound := map[string]any{
			"type":        "hysteria",
			"tag":         defaultTag(tag, "hysteria", server, port),
			"server":      server,
			"server_port": port,
			"tls":         tls,
		}
		if authString != "" {
			outbound["auth_str"] = authString
		}
		if up != "" {
			outbound["up"] = up
		}
		if down != "" {
			outbound["down"] = down
		}
		if obfs := strings.TrimSpace(getString(proxy, "obfs")); obfs != "" {
			outbound["obfs"] = obfs
		}
		if ports := normalizeHysteriaPortList(getString(proxy, "ports")); len(ports) > 0 {
			outbound["server_ports"] = ports
		}
		if recvWindowConn, ok := getUint(proxy, "recv-window-conn", "recv_window_conn"); ok {
			outbound["recv_window_conn"] = recvWindowConn
		}
		if recvWindow, ok := getUint(proxy, "recv-window", "recv_window"); ok {
			outbound["recv_window"] = recvWindow
		}
		if disableMTUDiscovery, ok := getBool(proxy, "disable-mtu-discovery", "disable_mtu_discovery"); ok {
			outbound["disable_mtu_discovery"] = disableMTUDiscovery
		}
		if hopInterval, ok := getDurationString(proxy, "s", "hop-interval", "hop_interval"); ok {
			outbound["hop_interval"] = hopInterval
		}
		if strings.EqualFold(strings.TrimSpace(getString(proxy, "protocol")), "udp") {
			outbound["network"] = "udp"
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "tuic":
		uuid := strings.TrimSpace(getString(proxy, "uuid"))
		if uuid == "" {
			return ParsedNode{}, false
		}
		sni := strings.TrimSpace(firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "servername"),
			getString(proxy, "server-name"),
		))
		insecure, _ := getBool(proxy, "skip-cert-verify", "allowInsecure", "insecure")
		tls := newClashEnabledTLS(sni, insecure, getStringSlice(proxy, "alpn"))
		if disableSNI, ok := getBool(proxy, "disable-sni", "disable_sni"); ok && disableSNI {
			tls["disable_sni"] = true
		}
		outbound := map[string]any{
			"type":        "tuic",
			"tag":         defaultTag(tag, "tuic", server, port),
			"server":      server,
			"server_port": port,
			"uuid":        uuid,
			"tls":         tls,
		}
		if password := strings.TrimSpace(getString(proxy, "password")); password != "" {
			outbound["password"] = password
		}
		if congestionControl := strings.TrimSpace(getString(proxy, "congestion-controller", "congestion_control")); congestionControl != "" {
			outbound["congestion_control"] = congestionControl
		}
		if udpRelayMode := strings.TrimSpace(getString(proxy, "udp-relay-mode", "udp_relay_mode")); udpRelayMode != "" {
			outbound["udp_relay_mode"] = udpRelayMode
		}
		if zeroRTT, ok := getBool(proxy, "reduce-rtt", "zero-rtt-handshake", "zero_rtt_handshake"); ok {
			outbound["zero_rtt_handshake"] = zeroRTT
		}
		if heartbeat, ok := getDurationString(proxy, "ms", "heartbeat-interval", "heartbeat_interval", "heartbeat"); ok {
			outbound["heartbeat"] = heartbeat
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "anytls":
		password := strings.TrimSpace(getString(proxy, "password"))
		if password == "" {
			return ParsedNode{}, false
		}
		sni := strings.TrimSpace(firstNonEmpty(
			getString(proxy, "sni"),
			getString(proxy, "servername"),
			getString(proxy, "server-name"),
		))
		insecure, _ := getBool(proxy, "skip-cert-verify", "allowInsecure", "insecure")
		tls := newClashEnabledTLS(sni, insecure, getStringSlice(proxy, "alpn"))
		if fingerprint := strings.TrimSpace(getString(proxy, "client-fingerprint", "client_fingerprint")); fingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": fingerprint,
			}
		}
		outbound := map[string]any{
			"type":        "anytls",
			"tag":         defaultTag(tag, "anytls", server, port),
			"server":      server,
			"server_port": port,
			"password":    password,
			"tls":         tls,
		}
		if interval, ok := getDurationString(proxy, "s", "idle-session-check-interval", "idle_session_check_interval"); ok {
			outbound["idle_session_check_interval"] = interval
		}
		if timeout, ok := getDurationString(proxy, "s", "idle-session-timeout", "idle_session_timeout"); ok {
			outbound["idle_session_timeout"] = timeout
		}
		if minIdle, ok := getUint(proxy, "min-idle-session", "min_idle_session"); ok {
			outbound["min_idle_session"] = minIdle
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	case "ssh":
		outbound := map[string]any{
			"type":        "ssh",
			"tag":         defaultTag(tag, "ssh", server, port),
			"server":      server,
			"server_port": port,
		}
		if user := strings.TrimSpace(firstNonEmpty(getString(proxy, "username"), getString(proxy, "user"))); user != "" {
			outbound["user"] = user
		}
		if password := strings.TrimSpace(getString(proxy, "password")); password != "" {
			outbound["password"] = password
		}
		if privateKey := strings.TrimSpace(getString(proxy, "private-key", "private_key")); privateKey != "" {
			outbound["private_key"] = privateKey
		}
		if passphrase := strings.TrimSpace(getString(proxy, "private-key-passphrase", "private_key_passphrase")); passphrase != "" {
			outbound["private_key_passphrase"] = passphrase
		}
		if hostKey := getStringList(proxy, "host-key", "host_key"); len(hostKey) > 0 {
			outbound["host_key"] = hostKey
		}
		if hostKeyAlgorithms := getStringList(proxy, "host-key-algorithms", "host_key_algorithms"); len(hostKeyAlgorithms) > 0 {
			outbound["host_key_algorithms"] = hostKeyAlgorithms
		}
		if clientVersion := strings.TrimSpace(getString(proxy, "client-version", "client_version")); clientVersion != "" {
			outbound["client_version"] = clientVersion
		}
		applyClashDialFields(outbound, proxy)
		return buildParsedNode(outbound)
	default:
		return ParsedNode{}, false
	}
}

func clashSOCKSVersion(nodeType string, proxy map[string]any) string {
	switch nodeType {
	case "socks4":
		return "4"
	case "socks4a":
		return "4a"
	case "socks5":
		return "5"
	}
	version := strings.TrimSpace(strings.ToLower(getString(proxy, "version")))
	switch version {
	case "4", "4a", "5":
		return version
	default:
		return ""
	}
}

func parseWireGuardLocalAddress(proxy map[string]any) []string {
	var addresses []string
	for _, key := range []string{"ip", "ipv6"} {
		for _, raw := range getStringList(proxy, key) {
			if normalized, ok := normalizeWireGuardPrefix(raw); ok {
				addresses = append(addresses, normalized)
			}
		}
	}
	return addresses
}

func parseWireGuardAllowedIPs(proxy map[string]any) []string {
	var allowedIPs []string
	for _, raw := range getStringList(proxy, "allowed-ips", "allowed_ips") {
		if _, _, err := net.ParseCIDR(raw); err == nil {
			allowedIPs = append(allowedIPs, raw)
		}
	}
	return allowedIPs
}

func normalizeWireGuardPrefix(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	if _, _, err := net.ParseCIDR(value); err == nil {
		return value, true
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return "", false
	}
	if ip.To4() != nil {
		return ip.String() + "/32", true
	}
	return ip.String() + "/128", true
}

func newClashEnabledTLS(serverName string, insecure bool, alpn []string) map[string]any {
	tls := map[string]any{
		"enabled": true,
	}
	if serverName = strings.TrimSpace(serverName); serverName != "" {
		tls["server_name"] = serverName
	}
	if insecure {
		tls["insecure"] = true
	}
	if len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	return tls
}

func splitCommaList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func normalizeHysteriaPortList(raw string) []string {
	ports := splitCommaList(raw)
	if len(ports) == 0 {
		return nil
	}
	out := make([]string, 0, len(ports))
	for _, port := range ports {
		out = append(out, normalizeHysteriaPortRange(port))
	}
	return out
}

func normalizeHysteriaPortRange(raw string) string {
	portRange := strings.TrimSpace(raw)
	if portRange == "" || strings.Contains(portRange, ":") {
		return portRange
	}
	compact := strings.ReplaceAll(portRange, " ", "")
	if strings.Count(compact, "-") != 1 {
		return portRange
	}
	start, end, ok := strings.Cut(compact, "-")
	if !ok {
		return portRange
	}
	if start != "" && !isDecimalString(start) {
		return portRange
	}
	if end != "" && !isDecimalString(end) {
		return portRange
	}
	if start == "" && end == "" {
		return portRange
	}
	return start + ":" + end
}

func isDecimalString(raw string) bool {
	if raw == "" {
		return false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeHysteriaRate(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if hasLetter(value) {
		return value
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value + " Mbps"
	}
	return value
}

func normalizeVLESSFlow(raw string) string {
	flow := strings.TrimSpace(raw)
	switch strings.ToLower(flow) {
	case "", "none", "null":
		return ""
	case "xtls-rprx-vision":
		return "xtls-rprx-vision"
	default:
		return ""
	}
}

func normalizeShadowsocksMethod(raw string) string {
	method := strings.TrimSpace(raw)
	if method == "" {
		return ""
	}
	upper := strings.ToUpper(strings.ReplaceAll(method, "-", "_"))
	switch upper {
	case "AEAD_CHACHA20_POLY1305":
		return "chacha20-ietf-poly1305"
	case "AEAD_AES_128_GCM":
		return "aes-128-gcm"
	case "AEAD_AES_192_GCM":
		return "aes-192-gcm"
	case "AEAD_AES_256_GCM":
		return "aes-256-gcm"
	}
	if strings.HasPrefix(upper, "AEAD_") {
		return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(upper, "AEAD_"), "_", "-"))
	}
	return strings.ToLower(method)
}

func applyClashDialFields(outbound map[string]any, proxy map[string]any) {
	if detour := strings.TrimSpace(getString(proxy, "dialer-proxy", "dialer_proxy")); detour != "" {
		outbound["detour"] = detour
	}
	if bindInterface := strings.TrimSpace(firstNonEmpty(
		getString(proxy, "bind-interface"),
		getString(proxy, "bind_interface"),
		getString(proxy, "interface-name"),
		getString(proxy, "interface_name"),
	)); bindInterface != "" {
		outbound["bind_interface"] = bindInterface
	}
	if routingMark, ok := getUint(proxy, "routing-mark", "routing_mark"); ok {
		outbound["routing_mark"] = routingMark
	} else if markText := strings.TrimSpace(getString(proxy, "routing-mark", "routing_mark")); markText != "" {
		outbound["routing_mark"] = markText
	}
	if tcpFastOpen, ok := getBool(proxy, "fast-open", "fast_open", "tfo"); ok {
		outbound["tcp_fast_open"] = tcpFastOpen
	}
	if tcpMultiPath, ok := getBool(proxy, "mptcp", "tcp-multi-path", "tcp_multi_path"); ok {
		outbound["tcp_multi_path"] = tcpMultiPath
	}
	if udpFragment, ok := getBool(proxy, "udp-fragment", "udp_fragment"); ok {
		outbound["udp_fragment"] = udpFragment
	}
	if domainStrategy := mapClashIPVersionToDomainStrategy(getString(proxy, "ip-version", "ip_version")); domainStrategy != "" {
		outbound["domain_strategy"] = domainStrategy
	}
}

func mapClashIPVersionToDomainStrategy(raw string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")) {
	case "ipv4":
		return "ipv4_only"
	case "ipv6":
		return "ipv6_only"
	case "prefer-ipv4":
		return "prefer_ipv4"
	case "prefer-ipv6":
		return "prefer_ipv6"
	default:
		return ""
	}
}

func getDurationString(m map[string]any, defaultUnit string, keys ...string) (string, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		if duration, ok := normalizeDurationValue(v, defaultUnit); ok {
			return duration, true
		}
	}
	return "", false
}

func normalizeDurationValue(raw any, defaultUnit string) (string, bool) {
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" {
		return "", false
	}
	if hasLetter(value) {
		return value, true
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		if defaultUnit == "" {
			return value, true
		}
		return value + defaultUnit, true
	}
	return "", false
}

func hasLetter(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == 'µ' {
			return true
		}
	}
	return false
}

func parseURILineSubscription(text string) ([]ParsedNode, bool) {
	var nodes []ParsedNode
	recognized := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		lower := strings.ToLower(line)
		var (
			node      ParsedNode
			ok        bool
			extraNode []ParsedNode
		)
		switch {
		case strings.HasPrefix(lower, "vmess://"):
			recognized = true
			node, ok = parseVmessURI(line)
		case strings.HasPrefix(lower, "vmess1://"):
			recognized = true
			node, ok = parseVmess1URI(line)
		case strings.HasPrefix(lower, "vless://"):
			recognized = true
			node, ok = parseVlessURI(line)
		case strings.HasPrefix(lower, "trojan://"):
			recognized = true
			node, ok = parseTrojanURI(line)
		case strings.HasPrefix(lower, "ss://"):
			recognized = true
			node, ok = parseSSURI(line)
		case strings.HasPrefix(lower, "hysteria2://"):
			recognized = true
			node, ok = parseHysteria2URI(line)
		case strings.HasPrefix(lower, "hy2://"):
			recognized = true
			node, ok = parseHysteria2URI(line)
		case strings.HasPrefix(lower, "ssd://"):
			recognized = true
			extraNode, ok = parseSSDURI(line)
		case strings.HasPrefix(lower, "socks://"):
			recognized = true
			node, ok = parseSocksURI(line)
			if !ok {
				node, ok = parseProxyURI(line)
			}
		case strings.HasPrefix(lower, "tg://socks"),
			strings.HasPrefix(lower, "https://t.me/socks"),
			strings.HasPrefix(lower, "tg://http"),
			strings.HasPrefix(lower, "https://t.me/http"),
			strings.HasPrefix(lower, "https://t.me/https"):
			recognized = true
			node, ok = parseTelegramProxyURI(line)
		case strings.HasPrefix(lower, "netch://"):
			recognized = true
			node, ok = parseNetchURI(line)
		case strings.HasPrefix(lower, "http://"),
			strings.HasPrefix(lower, "https://"),
			strings.HasPrefix(lower, "socks5://"),
			strings.HasPrefix(lower, "socks5h://"):
			recognized = true
			node, ok = parseProxyURI(line)
		default:
			node, ok = parsePlainHTTPProxyLine(line)
			if ok {
				recognized = true
			}
		}
		if len(extraNode) > 0 {
			nodes = append(nodes, extraNode...)
			continue
		}
		if ok {
			nodes = append(nodes, node)
		}
	}
	return nodes, recognized
}

func parsePlainHTTPProxyLine(line string) (ParsedNode, bool) {
	if strings.Contains(line, "://") {
		return ParsedNode{}, false
	}

	if node, ok := parseHTTPProxyIPPortUserPass(line); ok {
		return node, true
	}
	return parseHTTPProxyIPPort(line)
}

func parseProxyURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(uri)
	if err != nil {
		return ParsedNode{}, false
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	server := strings.TrimSpace(u.Hostname())
	if server == "" {
		return ParsedNode{}, false
	}
	if !isProxyURIPathAllowed(u.Path) {
		return ParsedNode{}, false
	}
	port, ok := parseRequiredURIPort(u)
	if !ok {
		return ParsedNode{}, false
	}

	nodeType := ""
	switch scheme {
	case "http":
		nodeType = "http"
	case "https":
		nodeType = "http"
	case "socks":
		nodeType = "socks"
	case "socks5", "socks5h":
		nodeType = "socks"
	default:
		return ParsedNode{}, false
	}
	if scheme != "https" && strings.TrimSpace(u.RawQuery) != "" {
		return ParsedNode{}, false
	}
	tag := decodeTag(u.Fragment)
	outbound := map[string]any{
		"type":        nodeType,
		"tag":         defaultTag(tag, nodeType, server, port),
		"server":      server,
		"server_port": port,
	}

	if u.User != nil {
		if username := strings.TrimSpace(u.User.Username()); username != "" {
			outbound["username"] = username
		}
		if password, ok := u.User.Password(); ok {
			outbound["password"] = password
		}
	}

	if scheme == "https" {
		query := u.Query()
		if !hasOnlyAllowedQueryKeys(query, "sni", "servername", "peer", "allowInsecure", "insecure") {
			return ParsedNode{}, false
		}
		tls := map[string]any{
			"enabled": true,
		}
		serverName := strings.TrimSpace(firstNonEmpty(
			query.Get("sni"),
			query.Get("servername"),
			query.Get("peer"),
			server,
		))
		if serverName != "" {
			tls["server_name"] = serverName
		}
		if queryBool(query, "allowInsecure", "insecure") {
			tls["insecure"] = true
		}
		outbound["tls"] = tls
	}

	return buildParsedNode(outbound)
}

func parseSocksURI(uri string) (ParsedNode, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(uri, "socks://"))
	if payload == "" {
		return ParsedNode{}, false
	}
	beforeFragment, fragment, _ := strings.Cut(payload, "#")
	tag := decodeTag(fragment)

	decoded, ok := decodeBase64Relaxed(strings.TrimSpace(beforeFragment))
	if !ok || !utf8.Valid(decoded) {
		return ParsedNode{}, false
	}

	decodedText := strings.TrimSpace(string(decoded))
	if decodedText == "" {
		return ParsedNode{}, false
	}

	var (
		server   string
		port     uint64
		username string
		password string
	)

	if at := strings.LastIndex(decodedText, "@"); at > 0 && at < len(decodedText)-1 {
		userInfo := strings.TrimSpace(decodedText[:at])
		hostPort := strings.TrimSpace(decodedText[at+1:])
		server, port, ok = parseHostPort(hostPort)
		if !ok {
			return ParsedNode{}, false
		}
		u, p, splitOK := strings.Cut(userInfo, ":")
		if !splitOK {
			return ParsedNode{}, false
		}
		username = strings.TrimSpace(u)
		password = strings.TrimSpace(p)
		if username == "" {
			return ParsedNode{}, false
		}
	} else {
		server, port, ok = parseHostPort(decodedText)
		if !ok {
			return ParsedNode{}, false
		}
	}

	outbound := map[string]any{
		"type":        "socks",
		"tag":         defaultTag(tag, "socks", server, port),
		"server":      server,
		"server_port": port,
	}
	if username != "" {
		outbound["username"] = username
	}
	if password != "" {
		outbound["password"] = password
	}
	return buildParsedNode(outbound)
}

func parseTelegramProxyURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(uri)
	if err != nil {
		return ParsedNode{}, false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	path := strings.Trim(strings.ToLower(strings.TrimSpace(u.Path)), "/")

	proxyType := ""
	tlsEnabled := false
	switch {
	case scheme == "tg" && strings.EqualFold(u.Host, "socks"):
		proxyType = "socks"
	case scheme == "tg" && strings.EqualFold(u.Host, "http"):
		proxyType = "http"
	case (scheme == "http" || scheme == "https") && host == "t.me":
		switch path {
		case "socks":
			proxyType = "socks"
		case "http":
			proxyType = "http"
		case "https":
			proxyType = "http"
			tlsEnabled = true
		default:
			return ParsedNode{}, false
		}
	default:
		return ParsedNode{}, false
	}

	query := u.Query()
	server := strings.TrimSpace(query.Get("server"))
	if server == "" {
		return ParsedNode{}, false
	}
	port, ok := parsePositiveUint64(query.Get("port"))
	if !ok || port == 0 {
		return ParsedNode{}, false
	}
	tag := strings.TrimSpace(firstNonEmpty(
		query.Get("remarks"),
		query.Get("remark"),
		query.Get("name"),
	))

	outbound := map[string]any{
		"type":        proxyType,
		"tag":         defaultTag(tag, proxyType, server, port),
		"server":      server,
		"server_port": port,
	}
	if username := strings.TrimSpace(query.Get("user")); username != "" {
		outbound["username"] = username
	}
	if password := strings.TrimSpace(query.Get("pass")); password != "" {
		outbound["password"] = password
	}
	if proxyType == "http" && tlsEnabled {
		outbound["tls"] = map[string]any{"enabled": true}
	}
	return buildParsedNode(outbound)
}

func parseNetchURI(uri string) (ParsedNode, bool) {
	trimmed := strings.TrimSpace(uri)
	schemeEnd := strings.Index(trimmed, "://")
	if schemeEnd <= 0 || !strings.EqualFold(strings.TrimSpace(trimmed[:schemeEnd]), "netch") {
		return ParsedNode{}, false
	}
	payload := strings.TrimSpace(trimmed[schemeEnd+3:])
	if payload == "" {
		return ParsedNode{}, false
	}
	decoded, ok := decodeBase64Relaxed(payload)
	if !ok || !utf8.Valid(decoded) {
		return ParsedNode{}, false
	}

	var nodeMap map[string]any
	if err := json.Unmarshal(decoded, &nodeMap); err != nil {
		return ParsedNode{}, false
	}

	proxyType := strings.ToLower(strings.TrimSpace(getString(nodeMap, "Type")))
	server := strings.TrimSpace(getString(nodeMap, "Hostname"))
	port, hasPort := getUint(nodeMap, "Port")
	if proxyType == "" || server == "" || !hasPort || port == 0 {
		return ParsedNode{}, false
	}
	tag := strings.TrimSpace(getString(nodeMap, "Remark"))

	switch proxyType {
	case "ss":
		method := normalizeShadowsocksMethod(getString(nodeMap, "EncryptMethod"))
		password := strings.TrimSpace(getString(nodeMap, "Password"))
		if method == "" || password == "" {
			return ParsedNode{}, false
		}
		outbound := map[string]any{
			"type":        "shadowsocks",
			"tag":         defaultTag(tag, "shadowsocks", server, port),
			"server":      server,
			"server_port": port,
			"method":      method,
			"password":    password,
		}
		plugin := strings.TrimSpace(getString(nodeMap, "Plugin"))
		pluginOpts := strings.TrimSpace(getString(nodeMap, "PluginOption"))
		plugin, pluginOpts = normalizeSSPlugin(plugin, pluginOpts)
		if plugin != "" {
			outbound["plugin"] = plugin
		}
		if pluginOpts != "" {
			outbound["plugin_opts"] = pluginOpts
		}
		return buildParsedNode(outbound)
	case "vmess":
		uuid := strings.TrimSpace(getString(nodeMap, "UserID"))
		if uuid == "" {
			return ParsedNode{}, false
		}
		security := strings.TrimSpace(getString(nodeMap, "EncryptMethod"))
		if security == "" {
			security = "auto"
		}
		outbound := map[string]any{
			"type":        "vmess",
			"tag":         defaultTag(tag, "vmess", server, port),
			"server":      server,
			"server_port": port,
			"uuid":        uuid,
			"security":    security,
			"alter_id":    uint64(0),
		}
		if alterID, ok := getUint(nodeMap, "AlterID"); ok {
			outbound["alter_id"] = alterID
		}
		if tlsSecure, ok := getBool(nodeMap, "TLSSecure"); ok && tlsSecure {
			tls := map[string]any{"enabled": true}
			if serverName := strings.TrimSpace(getString(nodeMap, "ServerName")); serverName != "" {
				tls["server_name"] = serverName
			}
			outbound["tls"] = tls
		}
		if !setV2RayTransportFromURI(
			outbound,
			getString(nodeMap, "TransferProtocol"),
			getString(nodeMap, "Path"),
			getString(nodeMap, "Host"),
			"",
		) {
			return ParsedNode{}, false
		}
		return buildParsedNode(outbound)
	case "socks5":
		outbound := map[string]any{
			"type":        "socks",
			"tag":         defaultTag(tag, "socks", server, port),
			"server":      server,
			"server_port": port,
		}
		if username := strings.TrimSpace(getString(nodeMap, "Username")); username != "" {
			outbound["username"] = username
		}
		if password := strings.TrimSpace(getString(nodeMap, "Password")); password != "" {
			outbound["password"] = password
		}
		return buildParsedNode(outbound)
	case "http", "https":
		outbound := map[string]any{
			"type":        "http",
			"tag":         defaultTag(tag, "http", server, port),
			"server":      server,
			"server_port": port,
		}
		if username := strings.TrimSpace(getString(nodeMap, "Username")); username != "" {
			outbound["username"] = username
		}
		if password := strings.TrimSpace(getString(nodeMap, "Password")); password != "" {
			outbound["password"] = password
		}
		if proxyType == "https" {
			outbound["tls"] = map[string]any{"enabled": true}
		}
		return buildParsedNode(outbound)
	case "trojan":
		password := strings.TrimSpace(getString(nodeMap, "Password"))
		if password == "" {
			return ParsedNode{}, false
		}
		tls := map[string]any{"enabled": true}
		if serverName := strings.TrimSpace(getString(nodeMap, "Host")); serverName != "" {
			tls["server_name"] = serverName
		}
		outbound := map[string]any{
			"type":        "trojan",
			"tag":         defaultTag(tag, "trojan", server, port),
			"server":      server,
			"server_port": port,
			"password":    password,
			"tls":         tls,
		}
		if !setV2RayTransportFromURI(
			outbound,
			getString(nodeMap, "TransferProtocol"),
			getString(nodeMap, "Path"),
			getString(nodeMap, "Host"),
			"",
		) {
			return ParsedNode{}, false
		}
		return buildParsedNode(outbound)
	default:
		return ParsedNode{}, false
	}
}

func parseRequiredURIPort(u *url.URL) (uint64, bool) {
	port := strings.TrimSpace(u.Port())
	if port == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func isProxyURIPathAllowed(path string) bool {
	trimmed := strings.TrimSpace(path)
	return trimmed == "" || trimmed == "/"
}

func hasOnlyAllowedQueryKeys(values url.Values, allowedKeys ...string) bool {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
	}
	for key := range values {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(key))]; !ok {
			return false
		}
	}
	return true
}
func parseHTTPProxyIPPort(line string) (ParsedNode, bool) {
	server, port, ok := parseHostPort(line)
	if !ok || net.ParseIP(server) == nil {
		return ParsedNode{}, false
	}

	outbound := map[string]any{
		"type":        "http",
		"tag":         defaultTag("", "http", server, port),
		"server":      server,
		"server_port": port,
	}
	return buildParsedNode(outbound)
}

func parseHTTPProxyIPPortUserPass(line string) (ParsedNode, bool) {
	server, port, username, password, ok := parseIPPortUserPass(line)
	if !ok {
		return ParsedNode{}, false
	}

	outbound := map[string]any{
		"type":        "http",
		"tag":         defaultTag("", "http", server, port),
		"server":      server,
		"server_port": port,
		"username":    username,
		"password":    password,
	}
	return buildParsedNode(outbound)
}

func parseIPPortUserPass(line string) (string, uint64, string, string, bool) {
	parts := strings.Split(line, ":")
	if len(parts) < 4 {
		return "", 0, "", "", false
	}

	password := strings.TrimSpace(parts[len(parts)-1])
	username := strings.TrimSpace(parts[len(parts)-2])
	portRaw := strings.TrimSpace(parts[len(parts)-3])
	hostRaw := strings.TrimSpace(strings.Join(parts[:len(parts)-3], ":"))
	if hostRaw == "" || username == "" || password == "" || portRaw == "" {
		return "", 0, "", "", false
	}

	port, err := strconv.ParseUint(portRaw, 10, 16)
	if err != nil {
		return "", 0, "", "", false
	}

	host := strings.Trim(strings.TrimSpace(hostRaw), "[]")
	if net.ParseIP(host) == nil {
		return "", 0, "", "", false
	}
	return host, port, username, password, true
}

func parseVmess1URI(uri string) (ParsedNode, bool) {
	trimmed := strings.TrimSpace(uri)
	if strings.HasPrefix(strings.ToLower(trimmed), "vmess1://") {
		trimmed = "vmess://" + trimmed[len("vmess1://"):]
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return ParsedNode{}, false
	}

	uuid := strings.TrimSpace(u.User.Username())
	server := strings.TrimSpace(u.Hostname())
	if uuid == "" || server == "" {
		return ParsedNode{}, false
	}

	port := uriPortOrDefault(u, 443)
	tag := decodeTag(u.Fragment)
	if tag == "" {
		tag = defaultTag("", "vmess", server, port)
	}

	query := u.Query()
	outbound := map[string]any{
		"type":        "vmess",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"uuid":        uuid,
		"security":    "auto",
		"alter_id":    uint64(0),
	}
	if security := strings.TrimSpace(firstNonEmpty(
		query.Get("security"),
		query.Get("cipher"),
		query.Get("scy"),
	)); security != "" {
		outbound["security"] = security
	}
	if alterID, ok := parsePositiveUint64(firstNonEmpty(
		query.Get("aid"),
		query.Get("alterId"),
		query.Get("alter_id"),
	)); ok {
		outbound["alter_id"] = alterID
	}

	tlsEnabled, hasTLS := parseSurgeBool(query.Get("tls"))
	insecure := queryBool(query, "allowInsecure", "insecure")
	alpn := splitALPN(query.Get("alpn"))
	fingerprint := strings.TrimSpace(firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	))
	sni := strings.TrimSpace(firstNonEmpty(
		query.Get("sni"),
		query.Get("servername"),
		query.Get("peer"),
		query.Get("host"),
		query.Get("ws.host"),
	))
	if (hasTLS && tlsEnabled) || insecure || len(alpn) > 0 || fingerprint != "" || sni != "" {
		tls := map[string]any{"enabled": true}
		if sni != "" {
			tls["server_name"] = sni
		}
		if insecure {
			tls["insecure"] = true
		}
		if len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		applyUTLSFromValue(tls, fingerprint)
		outbound["tls"] = tls
	}

	path := strings.TrimSpace(firstNonEmpty(query.Get("path"), query.Get("wspath")))
	if path == "" {
		path = strings.TrimSpace(u.Path)
	}
	if path == "/" {
		path = ""
	}
	if !setV2RayTransportFromURI(
		outbound,
		strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("network"), query.Get("net"), query.Get("type")))),
		path,
		strings.TrimSpace(firstNonEmpty(query.Get("ws.host"), query.Get("host"))),
		strings.TrimSpace(firstNonEmpty(
			query.Get("serviceName"),
			query.Get("service_name"),
			query.Get("service-name"),
			query.Get("grpc-service-name"),
			query.Get("grpc_service_name"),
		)),
	) {
		return ParsedNode{}, false
	}
	return buildParsedNode(outbound)
}

func parseSSDURI(uri string) ([]ParsedNode, bool) {
	trimmed := strings.TrimSpace(uri)
	prefixIndex := strings.Index(trimmed, "://")
	if prefixIndex < 0 || prefixIndex+3 >= len(trimmed) {
		return nil, false
	}
	payload := strings.TrimSpace(trimmed[prefixIndex+3:])
	if payload == "" {
		return nil, false
	}

	decoded, ok := decodeBase64Relaxed(payload)
	if !ok || !utf8.Valid(decoded) {
		return nil, false
	}

	var root map[string]any
	if err := json.Unmarshal(decoded, &root); err != nil {
		return nil, false
	}

	servers := getMapSlice(root, "servers")
	if len(servers) == 0 {
		return nil, false
	}

	defaultMethod := strings.TrimSpace(firstNonEmpty(
		getString(root, "encryption"),
		getString(root, "method"),
		getString(root, "cipher"),
	))
	defaultPassword := strings.TrimSpace(getString(root, "password"))
	defaultPort, _ := getUint(root, "port", "server_port")
	defaultPlugin := strings.TrimSpace(getString(root, "plugin"))
	defaultPluginOpts := strings.TrimSpace(firstNonEmpty(
		getString(root, "plugin_options"),
		getString(root, "plugin_opts"),
	))

	nodes := make([]ParsedNode, 0, len(servers))
	for _, server := range servers {
		address := strings.TrimSpace(firstNonEmpty(
			getString(server, "server"),
			getString(server, "address"),
		))
		if address == "" {
			continue
		}
		port, ok := getUint(server, "port", "server_port")
		if !ok {
			port = defaultPort
		}
		method := normalizeShadowsocksMethod(firstNonEmpty(
			getString(server, "encryption"),
			getString(server, "method"),
			getString(server, "cipher"),
			defaultMethod,
		))
		password := strings.TrimSpace(firstNonEmpty(
			getString(server, "password"),
			defaultPassword,
		))
		if port == 0 || method == "" || password == "" {
			continue
		}

		tag := strings.TrimSpace(firstNonEmpty(
			getString(server, "remarks"),
			getString(server, "name"),
			getString(server, "tag"),
		))
		outbound := map[string]any{
			"type":        "shadowsocks",
			"tag":         defaultTag(tag, "shadowsocks", address, port),
			"server":      address,
			"server_port": port,
			"method":      method,
			"password":    password,
		}

		plugin := strings.TrimSpace(firstNonEmpty(getString(server, "plugin"), defaultPlugin))
		pluginOpts := strings.TrimSpace(firstNonEmpty(
			getString(server, "plugin_options"),
			getString(server, "plugin_opts"),
			defaultPluginOpts,
		))
		plugin, pluginOpts = normalizeSSPlugin(plugin, pluginOpts)
		if plugin != "" {
			outbound["plugin"] = plugin
		}
		if pluginOpts != "" {
			outbound["plugin_opts"] = pluginOpts
		}

		if node, ok := buildParsedNode(outbound); ok {
			nodes = append(nodes, node)
		}
	}

	return nodes, len(nodes) > 0
}

func getMapSlice(m map[string]any, keys ...string) []map[string]any {
	for _, key := range keys {
		value, ok := m[key]
		if !ok || value == nil {
			continue
		}
		switch t := value.(type) {
		case []map[string]any:
			return t
		case []any:
			out := make([]map[string]any, 0, len(t))
			for _, item := range t {
				switch mapped := item.(type) {
				case map[string]any:
					out = append(out, mapped)
				case map[any]any:
					converted := make(map[string]any, len(mapped))
					for mk, mv := range mapped {
						converted[fmt.Sprint(mk)] = mv
					}
					out = append(out, converted)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func parseVmessURI(uri string) (ParsedNode, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(uri, "vmess://"))
	if payload == "" {
		return ParsedNode{}, false
	}
	beforeFragment, fragment, _ := strings.Cut(payload, "#")
	explicitTag := decodeTag(fragment)

	if decoded, ok := decodeBase64Relaxed(strings.TrimSpace(beforeFragment)); ok && utf8.Valid(decoded) {
		var v map[string]any
		if err := json.Unmarshal(decoded, &v); err == nil {
			return parseVmessFromMap(v, explicitTag)
		}
		if node, ok := parseVmessQuanPayload(string(decoded), explicitTag); ok {
			return node, true
		}
	}

	if node, ok := parseVmessShadowrocketURI(uri); ok {
		return node, true
	}
	if node, ok := parseVmessStdURI(uri); ok {
		return node, true
	}
	return ParsedNode{}, false
}

func parseVmessFromMap(v map[string]any, fallbackTag string) (ParsedNode, bool) {
	server := strings.TrimSpace(firstNonEmpty(
		getString(v, "add"),
		getString(v, "server"),
		getString(v, "address"),
	))
	uuid := strings.TrimSpace(firstNonEmpty(
		getString(v, "id"),
		getString(v, "uuid"),
		getString(v, "username"),
	))
	if server == "" || uuid == "" {
		return ParsedNode{}, false
	}

	port := uint64(443)
	if parsedPort, ok := getUint(v, "port", "server_port"); ok && parsedPort > 0 {
		port = parsedPort
	}
	tag := strings.TrimSpace(firstNonEmpty(
		getString(v, "ps"),
		getString(v, "name"),
		getString(v, "tag"),
		fallbackTag,
	))
	if tag == "" {
		tag = defaultTag("", "vmess", server, port)
	}
	security := strings.TrimSpace(firstNonEmpty(
		getString(v, "scy"),
		getString(v, "security"),
		getString(v, "cipher"),
	))
	if security == "" {
		security = "auto"
	}

	outbound := map[string]any{
		"type":        "vmess",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"uuid":        uuid,
		"security":    security,
	}
	if alterID, ok := getUint(v, "aid", "alterId", "alter_id"); ok {
		outbound["alter_id"] = alterID
	} else {
		outbound["alter_id"] = uint64(0)
	}

	tlsValue := strings.ToLower(strings.TrimSpace(getString(v, "tls")))
	insecureTLS, _ := getBool(v, "allowInsecure", "allow_insecure", "insecure", "skip-cert-verify", "skip_cert_verify")
	alpn := getStringSlice(v, "alpn")
	fingerprint := strings.TrimSpace(firstNonEmpty(
		getString(v, "fp"),
		getString(v, "fingerprint"),
		getString(v, "client-fingerprint"),
		getString(v, "client_fingerprint"),
	))
	if tlsValue == "tls" || tlsValue == "1" || tlsValue == "true" || insecureTLS || len(alpn) > 0 || fingerprint != "" {
		tls := map[string]any{"enabled": true}
		if sni := strings.TrimSpace(firstNonEmpty(
			getString(v, "sni"),
			getString(v, "servername"),
			getString(v, "peer"),
			getString(v, "host"),
			getString(v, "ws.host"),
		)); sni != "" {
			tls["server_name"] = sni
		}
		if insecureTLS {
			tls["insecure"] = true
		}
		if len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if fingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": fingerprint,
			}
		}
		outbound["tls"] = tls
	}

	network := vmessNetworkFromMap(v)
	if !setV2RayTransportFromURI(outbound, network,
		strings.TrimSpace(firstNonEmpty(
			getString(v, "path"),
			getString(v, "wspath"),
			getString(v, "ws-path"),
			getString(v, "ws_path"),
		)),
		strings.TrimSpace(firstNonEmpty(
			getString(v, "ws.host"),
			getString(v, "wsHost"),
			getString(v, "host"),
		)),
		strings.TrimSpace(firstNonEmpty(
			getString(v, "serviceName"),
			getString(v, "service_name"),
			getString(v, "service-name"),
			getString(v, "grpc-service-name"),
			getString(v, "grpc_service_name"),
		)),
	) {
		return ParsedNode{}, false
	}

	return buildParsedNode(outbound)
}

func vmessNetworkFromMap(v map[string]any) string {
	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		getString(v, "net"),
		getString(v, "network"),
	)))
	if network != "" {
		return network
	}

	// Some exports place transport under `type`, but in standard vmess JSON
	// `type` is often a header mode (`none/http`) rather than transport.
	candidate := strings.ToLower(strings.TrimSpace(getString(v, "type")))
	switch normalizeV2RayNetwork(candidate) {
	case "ws", "grpc", "h2", "quic", "httpupgrade", "kcp":
		return candidate
	default:
		return ""
	}
}

func parseVmessShadowrocketURI(uri string) (ParsedNode, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(uri, "vmess://"))
	if payload == "" {
		return ParsedNode{}, false
	}
	beforeFragment, fragment, _ := strings.Cut(payload, "#")
	encoded, rawQuery, hasQuery := strings.Cut(beforeFragment, "?")
	if !hasQuery || strings.TrimSpace(encoded) == "" {
		return ParsedNode{}, false
	}

	decoded, ok := decodeBase64Relaxed(strings.TrimSpace(encoded))
	if !ok || !utf8.Valid(decoded) {
		return ParsedNode{}, false
	}
	secret := strings.TrimSpace(string(decoded))
	left, hostPort, ok := strings.Cut(secret, "@")
	if !ok {
		return ParsedNode{}, false
	}
	cipher, uuid, ok := strings.Cut(left, ":")
	if !ok {
		return ParsedNode{}, false
	}
	server, port, ok := parseHostPort(hostPort)
	if !ok {
		return ParsedNode{}, false
	}

	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ParsedNode{}, false
	}

	nodeMap := map[string]any{
		"add":  server,
		"port": port,
		"id":   strings.TrimSpace(uuid),
		"scy":  strings.TrimSpace(cipher),
	}
	if aid, ok := parsePositiveUint64(firstNonEmpty(query.Get("aid"), query.Get("alterId"), query.Get("alter_id"))); ok {
		nodeMap["aid"] = aid
	}
	if tag := strings.TrimSpace(firstNonEmpty(decodeTag(fragment), query.Get("remarks"))); tag != "" {
		nodeMap["ps"] = tag
	}

	network := strings.ToLower(strings.TrimSpace(query.Get("network")))
	if obfs := strings.ToLower(strings.TrimSpace(query.Get("obfs"))); obfs == "websocket" || obfs == "ws" {
		network = "ws"
	}
	if network != "" {
		nodeMap["net"] = network
	}
	if path := strings.TrimSpace(firstNonEmpty(query.Get("path"), query.Get("wspath"))); path != "" {
		nodeMap["path"] = path
	}
	if host := strings.TrimSpace(firstNonEmpty(query.Get("obfsParam"), query.Get("wsHost"), query.Get("ws.host"), query.Get("host"))); host != "" {
		nodeMap["host"] = host
	}
	if tlsEnabled, ok := parseSurgeBool(query.Get("tls")); ok && tlsEnabled {
		nodeMap["tls"] = "tls"
	}
	if insecure := queryBool(query, "allowInsecure", "insecure"); insecure {
		nodeMap["allowInsecure"] = true
	}
	if sni := strings.TrimSpace(firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("servername"))); sni != "" {
		nodeMap["sni"] = sni
	}
	if alpn := strings.TrimSpace(query.Get("alpn")); alpn != "" {
		nodeMap["alpn"] = alpn
	}
	if fp := strings.TrimSpace(firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	)); fp != "" {
		nodeMap["fp"] = fp
	}
	return parseVmessFromMap(nodeMap, "")
}

func parseVmessStdURI(uri string) (ParsedNode, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(uri, "vmess://"))
	if payload == "" {
		return ParsedNode{}, false
	}
	beforeFragment, fragment, _ := strings.Cut(payload, "#")
	normalized := strings.ReplaceAll(beforeFragment, "/?", "?")
	core, rawQuery, _ := strings.Cut(normalized, "?")

	left, hostPort, ok := strings.Cut(core, "@")
	if !ok {
		return ParsedNode{}, false
	}
	netPart, userPart, ok := strings.Cut(left, ":")
	if !ok {
		return ParsedNode{}, false
	}
	server, port, ok := parseHostPort(hostPort)
	if !ok {
		return ParsedNode{}, false
	}

	splitIdx := strings.LastIndex(strings.TrimSpace(userPart), "-")
	if splitIdx <= 0 || splitIdx >= len(strings.TrimSpace(userPart))-1 {
		return ParsedNode{}, false
	}
	uuidAid := strings.TrimSpace(userPart)
	uuid := strings.TrimSpace(uuidAid[:splitIdx])
	aidText := strings.TrimSpace(uuidAid[splitIdx+1:])
	if uuid == "" {
		return ParsedNode{}, false
	}

	network := strings.ToLower(strings.TrimSpace(netPart))
	tlsFlag := false
	if plus := strings.Index(network, "+"); plus >= 0 {
		extras := strings.Split(network[plus+1:], "+")
		network = strings.TrimSpace(network[:plus])
		for _, extra := range extras {
			if strings.EqualFold(strings.TrimSpace(extra), "tls") {
				tlsFlag = true
			}
		}
	}
	if network == "" {
		network = "tcp"
	}

	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ParsedNode{}, false
	}

	nodeMap := map[string]any{
		"add": server,
		"id":  uuid,
		"net": network,
		"ps":  decodeTag(fragment),
		"scy": "auto",
	}
	nodeMap["port"] = port
	if aid, ok := parsePositiveUint64(aidText); ok {
		nodeMap["aid"] = aid
	}
	if tlsFlag {
		nodeMap["tls"] = "tls"
	}
	if security := strings.TrimSpace(firstNonEmpty(query.Get("security"), query.Get("cipher"))); security != "" {
		nodeMap["scy"] = security
	}
	if path := strings.TrimSpace(query.Get("path")); path != "" {
		nodeMap["path"] = path
	}
	if host := strings.TrimSpace(firstNonEmpty(query.Get("host"), query.Get("ws.host"))); host != "" {
		nodeMap["host"] = host
	}
	if sni := strings.TrimSpace(firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("servername"))); sni != "" {
		nodeMap["sni"] = sni
	}
	if insecure := queryBool(query, "allowInsecure", "insecure"); insecure {
		nodeMap["allowInsecure"] = true
	}
	if alpn := strings.TrimSpace(query.Get("alpn")); alpn != "" {
		nodeMap["alpn"] = alpn
	}
	if fp := strings.TrimSpace(firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	)); fp != "" {
		nodeMap["fp"] = fp
	}
	if serviceName := strings.TrimSpace(firstNonEmpty(
		query.Get("serviceName"),
		query.Get("service_name"),
		query.Get("service-name"),
		query.Get("grpc-service-name"),
		query.Get("grpc_service_name"),
	)); serviceName != "" {
		nodeMap["serviceName"] = serviceName
	}
	return parseVmessFromMap(nodeMap, "")
}

func parseVmessQuanPayload(payload string, fallbackTag string) (ParsedNode, bool) {
	left, right, ok := strings.Cut(payload, "=")
	if !ok {
		return ParsedNode{}, false
	}
	parts := splitCommaRespectQuotes(strings.TrimSpace(right))
	if len(parts) < 5 || !strings.EqualFold(strings.TrimSpace(parts[0]), "vmess") {
		return ParsedNode{}, false
	}

	server := strings.TrimSpace(parts[1])
	port, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 16)
	if err != nil || port == 0 {
		return ParsedNode{}, false
	}
	uuid := strings.Trim(strings.TrimSpace(parts[4]), `"'`)
	if server == "" || uuid == "" {
		return ParsedNode{}, false
	}

	tag := strings.TrimSpace(strings.Trim(left, `"'`))
	if tag == "" {
		tag = fallbackTag
	}

	nodeMap := map[string]any{
		"add":  server,
		"port": port,
		"id":   uuid,
		"ps":   tag,
	}
	if security := strings.Trim(strings.TrimSpace(parts[3]), `"'`); security != "" {
		nodeMap["scy"] = security
	}

	for _, rawPart := range parts[5:] {
		key, value, ok := strings.Cut(rawPart, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "aid", "alterid", "alter-id", "alter_id":
			if aid, ok := parsePositiveUint64(value); ok {
				nodeMap["aid"] = aid
			}
		case "over-tls", "tls":
			if enabled, ok := parseSurgeBool(value); ok && enabled {
				nodeMap["tls"] = "tls"
			}
		case "allowinsecure", "allow_insecure", "insecure", "skip-cert-verify":
			if enabled, ok := parseSurgeBool(value); ok && enabled {
				nodeMap["allowInsecure"] = true
			}
		case "tls-host", "sni", "peer", "servername":
			nodeMap["sni"] = value
		case "network", "net":
			nodeMap["net"] = value
		case "obfs":
			if strings.EqualFold(value, "ws") || strings.EqualFold(value, "websocket") {
				nodeMap["net"] = "ws"
			} else if value != "" {
				nodeMap["net"] = value
			}
		case "obfs-path", "path", "wspath":
			nodeMap["path"] = value
		case "host", "ws.host", "ws-host":
			nodeMap["host"] = value
		case "obfs-header", "ws-headers", "headers":
			if host := parseVmessHostFromHeaders(value); host != "" {
				nodeMap["host"] = host
			}
		}
	}

	return parseVmessFromMap(nodeMap, fallbackTag)
}

func parseVmessHostFromHeaders(raw string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(raw, "[RN]", "|"), "[rn]", "|")
	normalized = strings.ReplaceAll(strings.ReplaceAll(normalized, "\r", "|"), "\n", "|")
	headers := parseSurgeHeaderString(normalized)
	for k, v := range headers {
		if !strings.EqualFold(strings.TrimSpace(k), "Host") {
			continue
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func parseVlessURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(uri)
	if err != nil {
		return ParsedNode{}, false
	}
	uuid := strings.TrimSpace(u.User.Username())
	server := strings.TrimSpace(u.Hostname())
	if uuid == "" || server == "" {
		return ParsedNode{}, false
	}

	port := uriPortOrDefault(u, 443)
	tag := decodeTag(u.Fragment)
	if tag == "" {
		tag = defaultTag("", "vless", server, port)
	}

	query := u.Query()
	outbound := map[string]any{
		"type":        "vless",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"uuid":        uuid,
	}
	if flow := normalizeVLESSFlow(query.Get("flow")); flow != "" {
		outbound["flow"] = flow
	}

	security := strings.ToLower(strings.TrimSpace(query.Get("security")))
	sni := strings.TrimSpace(firstNonEmpty(query.Get("sni"), query.Get("servername")))
	insecure := queryBool(query, "allowInsecure", "insecure")
	alpn := splitALPN(query.Get("alpn"))
	fingerprint := strings.TrimSpace(firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	))
	if security == "tls" || security == "reality" || sni != "" || insecure || len(alpn) > 0 || fingerprint != "" {
		tls := map[string]any{"enabled": true}
		if sni != "" {
			tls["server_name"] = sni
		}
		if insecure {
			tls["insecure"] = true
		}
		if len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if security == "reality" {
			reality := map[string]any{"enabled": true}
			if publicKey := strings.TrimSpace(firstNonEmpty(
				query.Get("pbk"),
				query.Get("publicKey"),
				query.Get("public-key"),
				query.Get("public_key"),
			)); publicKey != "" {
				reality["public_key"] = publicKey
			}
			if shortID := strings.TrimSpace(firstNonEmpty(
				query.Get("sid"),
				query.Get("shortId"),
				query.Get("short-id"),
				query.Get("short_id"),
			)); shortID != "" {
				reality["short_id"] = shortID
			}
			tls["reality"] = reality
			if fingerprint == "" {
				fingerprint = "chrome"
			}
		}
		if fingerprint != "" {
			tls["utls"] = map[string]any{
				"enabled":     true,
				"fingerprint": fingerprint,
			}
		}
		outbound["tls"] = tls
	}

	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("network"))))
	if network == "" {
		if ws, ok := parseSurgeBool(query.Get("ws")); ok && ws {
			network = "ws"
		}
	}
	if !setV2RayTransportFromURI(outbound, network,
		strings.TrimSpace(firstNonEmpty(
			query.Get("path"),
			query.Get("wspath"),
			query.Get("ws-path"),
		)),
		strings.TrimSpace(firstNonEmpty(
			query.Get("host"),
			query.Get("ws.host"),
			query.Get("obfs-host"),
		)),
		strings.TrimSpace(firstNonEmpty(
			query.Get("serviceName"),
			query.Get("service_name"),
			query.Get("service-name"),
			query.Get("grpc-service-name"),
			query.Get("grpc_service_name"),
		)),
	) {
		return ParsedNode{}, false
	}

	return buildParsedNode(outbound)
}

func parseTrojanURI(uri string) (ParsedNode, bool) {
	u, err := url.Parse(uri)
	if err != nil {
		return ParsedNode{}, false
	}
	password := strings.TrimSpace(u.User.Username())
	server := strings.TrimSpace(u.Hostname())
	if password == "" || server == "" {
		return ParsedNode{}, false
	}

	port := uriPortOrDefault(u, 443)
	tag := decodeTag(u.Fragment)
	if tag == "" {
		tag = defaultTag("", "trojan", server, port)
	}

	query := u.Query()
	serverName := strings.TrimSpace(firstNonEmpty(
		query.Get("sni"),
		query.Get("peer"),
		query.Get("servername"),
		server,
	))
	insecure := queryBool(query, "allowInsecure", "insecure")
	alpn := splitALPN(query.Get("alpn"))
	fingerprint := strings.TrimSpace(firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	))

	tls := map[string]any{
		"enabled":     true,
		"server_name": serverName,
	}
	if insecure {
		tls["insecure"] = true
	}
	if len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if fingerprint != "" {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": fingerprint,
		}
	}

	outbound := map[string]any{
		"type":        "trojan",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"password":    password,
		"tls":         tls,
	}

	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("network"))))
	if network == "" {
		if ws, ok := parseSurgeBool(query.Get("ws")); ok && ws {
			network = "ws"
		}
	}
	if !setV2RayTransportFromURI(outbound, network,
		strings.TrimSpace(firstNonEmpty(
			query.Get("path"),
			query.Get("wspath"),
			query.Get("ws-path"),
		)),
		strings.TrimSpace(firstNonEmpty(
			query.Get("host"),
			query.Get("ws.host"),
			query.Get("obfs-host"),
		)),
		strings.TrimSpace(firstNonEmpty(
			query.Get("serviceName"),
			query.Get("service_name"),
			query.Get("service-name"),
			query.Get("grpc-service-name"),
			query.Get("grpc_service_name"),
		)),
	) {
		return ParsedNode{}, false
	}

	return buildParsedNode(outbound)
}

func parseHysteria2URI(uri string) (ParsedNode, bool) {
	normalized := strings.TrimSpace(uri)
	if strings.HasPrefix(strings.ToLower(normalized), "hy2://") {
		normalized = "hysteria2://" + normalized[len("hy2://"):]
	}

	u, err := url.Parse(normalized)
	if err != nil {
		return ParsedNode{}, false
	}
	query := u.Query()
	password := ""
	if u.User != nil {
		username := strings.TrimSpace(u.User.Username())
		if parsedPassword, ok := u.User.Password(); ok {
			password = strings.TrimSpace(username + ":" + parsedPassword)
		} else {
			password = username
		}
	}
	if password == "" {
		password = strings.TrimSpace(firstNonEmpty(query.Get("password"), query.Get("auth")))
	}
	server := strings.TrimSpace(u.Hostname())
	if password == "" || server == "" {
		return ParsedNode{}, false
	}

	port := uriPortOrDefault(u, 443)
	tag := decodeTag(u.Fragment)
	if tag == "" {
		tag = defaultTag("", "hysteria2", server, port)
	}

	serverName := strings.TrimSpace(firstNonEmpty(
		query.Get("sni"),
		query.Get("peer"),
		query.Get("servername"),
		server,
	))
	tls := map[string]any{
		"enabled":     true,
		"server_name": serverName,
	}
	if queryBool(query, "insecure", "allowInsecure") {
		tls["insecure"] = true
	}
	if alpn := splitALPN(query.Get("alpn")); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	applyUTLSFromValue(tls, firstNonEmpty(
		query.Get("fp"),
		query.Get("fingerprint"),
		query.Get("client-fingerprint"),
		query.Get("client_fingerprint"),
	))
	if pins := splitCommaList(firstNonEmpty(
		query.Get("pinSHA256"),
		query.Get("pin-sha256"),
		query.Get("pin_sha256"),
	)); len(pins) > 0 {
		tls["certificate_public_key_sha256"] = pins
	}
	if certPath := strings.TrimSpace(query.Get("ca")); certPath != "" {
		tls["certificate_path"] = certPath
	}
	if cert := strings.TrimSpace(firstNonEmpty(query.Get("ca-str"), query.Get("ca_str"))); cert != "" {
		tls["certificate"] = []string{cert}
	}

	outbound := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"password":    password,
		"tls":         tls,
	}
	if ports := normalizeHysteriaPortList(firstNonEmpty(query.Get("ports"), query.Get("mport"))); len(ports) > 0 {
		outbound["server_ports"] = ports
	}
	if upMbps, ok := parsePositiveUint64(
		firstNonEmpty(query.Get("upmbps"), query.Get("up_mbps"), query.Get("up")),
	); ok {
		outbound["up_mbps"] = upMbps
	}
	if downMbps, ok := parsePositiveUint64(
		firstNonEmpty(query.Get("downmbps"), query.Get("down_mbps"), query.Get("down")),
	); ok {
		outbound["down_mbps"] = downMbps
	}
	if hopInterval, ok := normalizeDurationValue(
		firstNonEmpty(query.Get("hopInterval"), query.Get("hop-interval"), query.Get("hop_interval")),
		"s",
	); ok {
		outbound["hop_interval"] = hopInterval
	}
	if obfsType := strings.TrimSpace(query.Get("obfs")); obfsType != "" {
		obfs := map[string]any{"type": obfsType}
		if obfsPassword := strings.TrimSpace(firstNonEmpty(
			query.Get("obfs-password"),
			query.Get("obfs_password"),
		)); obfsPassword != "" {
			obfs["password"] = obfsPassword
		}
		outbound["obfs"] = obfs
	}
	return buildParsedNode(outbound)
}

func parseSSURI(uri string) (ParsedNode, bool) {
	raw := strings.TrimSpace(strings.TrimPrefix(uri, "ss://"))
	if raw == "" {
		return ParsedNode{}, false
	}

	beforeFragment, fragment, _ := strings.Cut(raw, "#")
	beforeQuery, rawQuery, _ := strings.Cut(beforeFragment, "?")
	tag := decodeTag(fragment)
	plugin, pluginOpts := parseSSPluginFromQuery(rawQuery)

	if at := strings.LastIndex(beforeQuery, "@"); at > 0 && at < len(beforeQuery)-1 {
		left := beforeQuery[:at]
		hostport := beforeQuery[at+1:]
		method, password, ok := parseSSMethodPassword(left)
		if !ok {
			return ParsedNode{}, false
		}
		server, port, ok := parseHostPort(hostport)
		if !ok {
			return ParsedNode{}, false
		}
		outbound := map[string]any{
			"type":        "shadowsocks",
			"tag":         defaultTag(tag, "shadowsocks", server, port),
			"server":      server,
			"server_port": port,
			"method":      method,
			"password":    password,
		}
		if plugin != "" {
			outbound["plugin"] = plugin
		}
		if pluginOpts != "" {
			outbound["plugin_opts"] = pluginOpts
		}
		return buildParsedNode(outbound)
	}

	decoded, ok := decodeBase64Relaxed(beforeQuery)
	if !ok || !utf8.Valid(decoded) {
		return ParsedNode{}, false
	}
	decodedText := string(decoded)
	at := strings.LastIndex(decodedText, "@")
	if at <= 0 || at >= len(decodedText)-1 {
		return ParsedNode{}, false
	}
	left := decodedText[:at]
	hostport := decodedText[at+1:]
	method, password, ok := parseSSMethodPassword(left)
	if !ok {
		return ParsedNode{}, false
	}
	server, port, ok := parseHostPort(hostport)
	if !ok {
		return ParsedNode{}, false
	}

	outbound := map[string]any{
		"type":        "shadowsocks",
		"tag":         defaultTag(tag, "shadowsocks", server, port),
		"server":      server,
		"server_port": port,
		"method":      method,
		"password":    password,
	}
	if plugin != "" {
		outbound["plugin"] = plugin
	}
	if pluginOpts != "" {
		outbound["plugin_opts"] = pluginOpts
	}
	return buildParsedNode(outbound)
}

func parseSSMethodPassword(input string) (string, string, bool) {
	if method, password, ok := strings.Cut(input, ":"); ok {
		method = normalizeShadowsocksMethod(method)
		password = strings.TrimSpace(password)
		if method != "" && password != "" {
			return method, password, true
		}
	}

	decoded, ok := decodeBase64Relaxed(strings.TrimSpace(input))
	if !ok || !utf8.Valid(decoded) {
		return "", "", false
	}
	method, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return "", "", false
	}
	method = normalizeShadowsocksMethod(method)
	password = strings.TrimSpace(password)
	if method == "" || password == "" {
		return "", "", false
	}
	return method, password, true
}

func parseSSPluginFromQuery(rawQuery string) (plugin string, pluginOpts string) {
	rawQuery = strings.TrimSpace(rawQuery)
	if rawQuery == "" {
		return "", ""
	}
	query, err := url.ParseQuery(rawQuery)
	if err == nil {
		if pluginSpec := strings.TrimSpace(query.Get("plugin")); pluginSpec != "" {
			plugin, pluginOpts = splitSSPluginSpec(pluginSpec)
			if plugin != "" {
				rawOpts := strings.TrimSpace(firstNonEmpty(
					query.Get("plugin-opts"),
					query.Get("plugin_opts"),
				))
				if pluginOpts == "" && rawOpts != "" {
					if optsPlugin, optsValue := splitSSPluginSpec(rawOpts); optsPlugin != "" && optsValue != "" && canonicalSSPluginName(optsPlugin) == canonicalSSPluginName(plugin) {
						pluginOpts = optsValue
					} else {
						pluginOpts = rawOpts
					}
				}
				return normalizeSSPlugin(plugin, pluginOpts)
			}
		}
		if spec := strings.TrimSpace(firstNonEmpty(query.Get("plugin-opts"), query.Get("plugin_opts"))); spec != "" {
			plugin, pluginOpts = splitSSPluginSpec(spec)
			return normalizeSSPlugin(plugin, pluginOpts)
		}
	}

	spec := extractSSPluginSpecFromRawQuery(rawQuery)
	if spec == "" {
		return "", ""
	}
	plugin, pluginOpts = splitSSPluginSpec(spec)
	return normalizeSSPlugin(plugin, pluginOpts)
}

func extractSSPluginSpecFromRawQuery(rawQuery string) string {
	for _, pair := range strings.Split(rawQuery, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "plugin", "plugin-opts", "plugin_opts":
		default:
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func splitSSPluginSpec(spec string) (plugin string, pluginOpts string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ""
	}
	parts := splitUnescaped(spec, ';')
	plugin = strings.TrimSpace(parts[0])
	if plugin == "" {
		return "", ""
	}
	if len(parts) == 1 {
		return plugin, ""
	}
	rest := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part != "" {
			rest = append(rest, part)
		}
	}
	return plugin, strings.Join(rest, ";")
}

func normalizeSSPlugin(plugin string, pluginOpts string) (string, string) {
	plugin = strings.TrimSpace(plugin)
	pluginOpts = strings.TrimSpace(pluginOpts)
	switch canonicalSSPluginName(plugin) {
	case "obfs-local":
		return "obfs-local", normalizeSimpleObfsOptions(pluginOpts)
	default:
		return plugin, pluginOpts
	}
}

func canonicalSSPluginName(plugin string) string {
	switch strings.ToLower(strings.TrimSpace(plugin)) {
	case "obfs", "simple-obfs", "obfs-local":
		return "obfs-local"
	default:
		return strings.ToLower(strings.TrimSpace(plugin))
	}
}

func normalizeSimpleObfsOptions(pluginOpts string) string {
	if pluginOpts == "" {
		return ""
	}
	parts := splitUnescaped(pluginOpts, ';')
	var obfsValue string
	var hostValue string
	extras := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, hasValue := cutUnescaped(part, '=')
		if !hasValue {
			if canonicalSSPluginName(part) == "obfs-local" {
				continue
			}
			extras = append(extras, part)
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "mode", "obfs":
			if value != "" && obfsValue == "" {
				obfsValue = value
			}
		case "host", "obfs-host", "obfs_host":
			if value != "" && hostValue == "" {
				hostValue = value
			}
		default:
			extras = append(extras, key+"="+value)
		}
	}
	normalized := make([]string, 0, 2+len(extras))
	if obfsValue != "" {
		normalized = append(normalized, "obfs="+obfsValue)
	}
	if hostValue != "" {
		normalized = append(normalized, "obfs-host="+hostValue)
	}
	normalized = append(normalized, extras...)
	return strings.Join(normalized, ";")
}

func splitUnescaped(input string, delimiter byte) []string {
	parts := make([]string, 0, 1)
	start := 0
	escaped := false
	for i := 0; i < len(input); i++ {
		switch {
		case escaped:
			escaped = false
		case input[i] == '\\':
			escaped = true
		case input[i] == delimiter:
			parts = append(parts, input[start:i])
			start = i + 1
		}
	}
	return append(parts, input[start:])
}

func cutUnescaped(input string, delimiter byte) (string, string, bool) {
	escaped := false
	for i := 0; i < len(input); i++ {
		switch {
		case escaped:
			escaped = false
		case input[i] == '\\':
			escaped = true
		case input[i] == delimiter:
			return input[:i], input[i+1:], true
		}
	}
	return input, "", false
}

func parsePositiveUint64(raw string) (uint64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
}

func parseHostPort(hostport string) (string, uint64, bool) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", 0, false
	}

	if host, port, err := net.SplitHostPort(hostport); err == nil {
		parsedPort, parseErr := strconv.ParseUint(strings.TrimSpace(port), 10, 16)
		if parseErr != nil {
			return "", 0, false
		}
		host = strings.TrimSpace(strings.Trim(host, "[]"))
		if host == "" {
			return "", 0, false
		}
		return host, parsedPort, true
	}

	idx := strings.LastIndex(hostport, ":")
	if idx <= 0 || idx >= len(hostport)-1 {
		return "", 0, false
	}
	host := strings.TrimSpace(strings.Trim(hostport[:idx], "[]"))
	if host == "" {
		return "", 0, false
	}
	parsedPort, err := strconv.ParseUint(strings.TrimSpace(hostport[idx+1:]), 10, 16)
	if err != nil {
		return "", 0, false
	}
	return host, parsedPort, true
}

func decodeBase64Relaxed(input string) ([]byte, bool) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, false
	}

	if rem := len(s) % 4; rem != 0 {
		s += strings.Repeat("=", 4-rem)
	}
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, true
	}
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, true
	}
	return nil, false
}

func tryDecodeBase64ToText(data []byte) (string, bool) {
	compact := strings.Join(strings.Fields(string(data)), "")
	if !looksLikeBase64(compact) {
		return "", false
	}

	decoded, ok := decodeBase64Relaxed(compact)
	if !ok || !utf8.Valid(decoded) {
		return "", false
	}
	return string(decoded), true
}

func looksLikeBase64(s string) bool {
	if len(s) < 24 || len(s)%4 == 1 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '+' || r == '/' || r == '-' || r == '_' || r == '=':
		default:
			return false
		}
	}
	return true
}

func looksLikeJSON(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	switch data[0] {
	case '{':
		return true
	case '[':
		// Avoid misclassifying bracketed IPv6 proxy lines like:
		// [2001:db8::1]:8080
		idx := 1
		for idx < len(data) {
			switch data[idx] {
			case ' ', '\t', '\r', '\n':
				idx++
				continue
			case '{', ']':
				return true
			default:
				return false
			}
		}
		return false
	default:
		return false
	}
}

func looksLikeClashYAML(text string) bool {
	lower := strings.ToLower(text)
	return strings.HasPrefix(lower, "proxies:") ||
		strings.Contains(lower, "\nproxies:") ||
		strings.HasPrefix(lower, "proxy:") ||
		strings.Contains(lower, "\nproxy:") ||
		strings.HasPrefix(lower, "proxy-groups:") ||
		strings.Contains(lower, "\nproxy-groups:")
}

func setTLSFromClash(outbound map[string]any, proxy map[string]any, key string) {
	enabled, ok := getBool(proxy, key)
	if !ok || !enabled {
		return
	}
	tls := map[string]any{"enabled": true}
	if serverName := strings.TrimSpace(firstNonEmpty(
		getString(proxy, "servername"),
		getString(proxy, "sni"),
		getString(proxy, "peer"),
	)); serverName != "" {
		tls["server_name"] = serverName
	}
	if insecure, ok := getBool(proxy, "skip-cert-verify", "insecure", "allowInsecure"); ok && insecure {
		tls["insecure"] = true
	}
	if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	applyUTLSFromValue(tls, firstNonEmpty(
		getString(proxy, "fingerprint"),
		getString(proxy, "client-fingerprint"),
		getString(proxy, "client_fingerprint"),
		getString(proxy, "fp"),
	))
	applyClashRealityToTLS(tls, proxy)
	applyTLSCertificateFromClash(tls, proxy)
	outbound["tls"] = tls
}

func applyClashRealityToTLS(tls map[string]any, proxy map[string]any) {
	var realitySource map[string]any
	if realityOpts, ok := getMap(proxy, "reality-opts", "reality_opts"); ok {
		realitySource = realityOpts
	} else {
		realitySource = proxy
	}

	publicKey := strings.TrimSpace(firstNonEmpty(
		getString(realitySource, "public-key"),
		getString(realitySource, "public_key"),
		getString(realitySource, "pbk"),
	))
	shortID := strings.TrimSpace(firstNonEmpty(
		getString(realitySource, "short-id"),
		getString(realitySource, "short_id"),
		getString(realitySource, "sid"),
	))
	if publicKey == "" && shortID == "" {
		return
	}

	reality := map[string]any{"enabled": true}
	if publicKey != "" {
		reality["public_key"] = publicKey
	}
	if shortID != "" {
		reality["short_id"] = shortID
	}
	tls["reality"] = reality

	if _, hasUTLS := tls["utls"]; !hasUTLS {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": "chrome",
		}
	}
}

func applyUTLSFromValue(tls map[string]any, rawFingerprint string) {
	fingerprint := strings.TrimSpace(rawFingerprint)
	if fingerprint == "" {
		return
	}
	tls["utls"] = map[string]any{
		"enabled":     true,
		"fingerprint": fingerprint,
	}
}

func applyTLSCertificateFromClash(tls map[string]any, proxy map[string]any) {
	if certificatePath := strings.TrimSpace(getString(proxy, "ca")); certificatePath != "" {
		tls["certificate_path"] = certificatePath
	}
	if certificate := strings.TrimSpace(firstNonEmpty(getString(proxy, "ca-str"), getString(proxy, "ca_str"))); certificate != "" {
		tls["certificate"] = []string{certificate}
	}
}

func setSSPluginFromClash(outbound map[string]any, proxy map[string]any) {
	plugin := strings.TrimSpace(getString(proxy, "plugin"))
	pluginOpts := strings.TrimSpace(getString(proxy, "plugin-opts-string", "plugin_opts_string", "plugin-opts", "plugin_opts"))
	if pluginOpts == "" {
		if optsMap, ok := getMap(proxy, "plugin-opts", "plugin_opts"); ok && len(optsMap) > 0 {
			pluginOpts = buildPluginOptionsString(optsMap)
		}
	}

	if plugin == "" {
		if obfsMode := strings.TrimSpace(getString(proxy, "obfs")); obfsMode != "" {
			plugin = "obfs-local"
			var opts []string
			opts = append(opts, "obfs="+obfsMode)
			if obfsHost := strings.TrimSpace(getString(proxy, "obfs-host", "obfs_host")); obfsHost != "" {
				opts = append(opts, "obfs-host="+obfsHost)
			}
			pluginOpts = strings.Join(opts, ";")
		}
	}

	if plugin == "" {
		return
	}
	plugin, pluginOpts = normalizeSSPlugin(plugin, pluginOpts)
	outbound["plugin"] = plugin
	if pluginOpts != "" {
		outbound["plugin_opts"] = pluginOpts
	}
}

func buildPluginOptionsString(opts map[string]any) string {
	if len(opts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(opts))
	for key := range opts {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		rawValue, ok := opts[key]
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "tls":
			if enabled, ok := parseBoolValue(rawValue); ok {
				if enabled {
					parts = append(parts, key)
				}
				continue
			}
		case "mux":
			if enabled, ok := parseBoolValue(rawValue); ok {
				if enabled {
					parts = append(parts, key+"=4")
				}
				continue
			}
		}
		value := pluginOptionValueToString(rawValue)
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, ";")
}

func pluginOptionValueToString(value any) string {
	switch t := value.(type) {
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []string:
		items := make([]string, 0, len(t))
		for _, item := range t {
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		}
		return strings.Join(items, ",")
	case []any:
		items := make([]string, 0, len(t))
		for _, item := range t {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				items = append(items, text)
			}
		}
		return strings.Join(items, ",")
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func parseBoolValue(value any) (bool, bool) {
	switch t := value.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	case float64:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	case int:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	}
	return false, false
}

func normalizeV2RayNetwork(raw string) string {
	network := strings.ToLower(strings.TrimSpace(raw))
	switch network {
	case "none", "raw":
		return "tcp"
	case "websocket":
		return "ws"
	case "http-upgrade":
		return "httpupgrade"
	case "mkcp":
		return "kcp"
	default:
		return network
	}
}

func setV2RayTransportFromClash(outbound map[string]any, proxy map[string]any) bool {
	network := normalizeV2RayNetwork(getString(proxy, "network"))
	switch network {
	case "", "tcp":
		return true
	case "ws":
		setWSTransportFromClash(outbound, proxy)
		normalizeTLSALPNForWSTransport(outbound)
		return true
	case "grpc":
		transport := map[string]any{"type": "grpc"}
		if grpcOpts, ok := getMap(proxy, "grpc-opts", "grpc_opts"); ok {
			serviceName := strings.TrimSpace(firstNonEmpty(
				getString(grpcOpts, "grpc-service-name"),
				getString(grpcOpts, "service-name"),
				getString(grpcOpts, "service_name"),
			))
			if serviceName != "" {
				transport["service_name"] = serviceName
			}
		}
		outbound["transport"] = transport
		return true
	case "h2", "http":
		transport := map[string]any{"type": "http"}
		if network == "h2" {
			if h2Opts, ok := getMap(proxy, "h2-opts", "h2_opts"); ok {
				setHTTPTransportFromClashOptions(transport, h2Opts)
			}
		} else {
			if httpOpts, ok := getMap(proxy, "http-opts", "http_opts"); ok {
				setHTTPTransportFromClashOptions(transport, httpOpts)
			}
		}
		outbound["transport"] = transport
		return true
	case "quic":
		outbound["transport"] = map[string]any{"type": "quic"}
		return true
	case "kcp":
		// sing-box v1.12.x does not support mKCP transport.
		return false
	case "httpupgrade", "http-upgrade":
		transport := map[string]any{"type": "httpupgrade"}
		if opts, ok := getMap(proxy, "http-upgrade-opts", "http_upgrade_opts", "http-opts", "http_opts"); ok {
			if path := firstNonEmptyValue(opts["path"]); path != "" {
				transport["path"] = path
			}
			if host := firstNonEmptyValue(opts["host"]); host != "" {
				transport["host"] = host
			}
			if headers, ok := getMap(opts, "headers"); ok && len(headers) > 0 {
				transport["headers"] = headers
			}
		}
		outbound["transport"] = transport
		return true
	default:
		return false
	}
}

func setHTTPTransportFromClashOptions(transport map[string]any, opts map[string]any) {
	if path := firstNonEmptyValue(opts["path"]); path != "" {
		transport["path"] = path
	}

	if hosts := parseStringListValue(opts["host"]); len(hosts) > 0 {
		transport["host"] = hosts
		return
	}
	headers, ok := getMap(opts, "headers")
	if !ok {
		return
	}
	if hosts := parseStringListValue(firstNonNil(headers["Host"], headers["host"])); len(hosts) > 0 {
		transport["host"] = hosts
	}
}

func setV2RayTransportFromURI(outbound map[string]any, network string, rawPath string, rawHost string, rawServiceName string) bool {
	switch normalizeV2RayNetwork(network) {
	case "", "tcp":
		return true
	case "ws":
		transport := map[string]any{"type": "ws"}
		if path := strings.TrimSpace(rawPath); path != "" {
			setWSPathAndEarlyData(transport, path)
		}
		if host := strings.TrimSpace(rawHost); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		outbound["transport"] = transport
		normalizeTLSALPNForWSTransport(outbound)
		return true
	case "grpc":
		transport := map[string]any{"type": "grpc"}
		serviceName := strings.TrimSpace(rawServiceName)
		if serviceName == "" {
			serviceName = strings.TrimSpace(rawPath)
		}
		if serviceName != "" {
			transport["service_name"] = serviceName
		}
		outbound["transport"] = transport
		return true
	case "h2", "http":
		transport := map[string]any{"type": "http"}
		if path := strings.TrimSpace(rawPath); path != "" {
			transport["path"] = path
		}
		if hosts := splitCommaList(rawHost); len(hosts) > 0 {
			transport["host"] = hosts
		}
		outbound["transport"] = transport
		return true
	case "quic":
		outbound["transport"] = map[string]any{"type": "quic"}
		return true
	case "kcp":
		// sing-box v1.12.x does not support mKCP transport.
		return false
	case "httpupgrade", "http-upgrade":
		transport := map[string]any{"type": "httpupgrade"}
		if path := strings.TrimSpace(rawPath); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(rawHost); host != "" {
			transport["host"] = host
		}
		outbound["transport"] = transport
		return true
	default:
		return false
	}
}

func normalizeTLSALPNForWSTransport(outbound map[string]any) {
	tls, ok := outbound["tls"].(map[string]any)
	if !ok {
		return
	}
	rawALPN, ok := tls["alpn"]
	if !ok {
		return
	}

	var keep []string
	switch values := rawALPN.(type) {
	case []string:
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(value), "http/1.1") {
				keep = append(keep, "http/1.1")
			}
		}
	case []any:
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "http/1.1") {
				keep = append(keep, "http/1.1")
			}
		}
	}

	if len(keep) == 0 {
		delete(tls, "alpn")
		return
	}
	tls["alpn"] = keep
}

func firstNonEmptyValue(value any) string {
	switch t := value.(type) {
	case string:
		return strings.TrimSpace(t)
	case []string:
		for _, item := range t {
			if item = strings.TrimSpace(item); item != "" {
				return item
			}
		}
	case []any:
		for _, item := range t {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" {
				return text
			}
		}
	default:
		if value != nil {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				return text
			}
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func setWSTransportFromClash(outbound map[string]any, proxy map[string]any) {
	if normalizeV2RayNetwork(getString(proxy, "network")) != "ws" {
		return
	}
	transport := map[string]any{"type": "ws"}
	if wsOpts, ok := getMap(proxy, "ws-opts", "ws_opts"); ok {
		if path := strings.TrimSpace(getString(wsOpts, "path")); path != "" {
			setWSPathAndEarlyData(transport, path)
		}
		if headers, ok := getMap(wsOpts, "headers"); ok && len(headers) > 0 {
			transport["headers"] = headers
		}
	}
	if path := strings.TrimSpace(getString(proxy, "ws-path", "ws_path")); path != "" {
		setWSPathAndEarlyData(transport, path)
	}
	if headers, ok := getMap(proxy, "ws-headers", "ws_headers"); ok && len(headers) > 0 {
		transport["headers"] = headers
	}
	outbound["transport"] = transport
}

func setWSPathAndEarlyData(transport map[string]any, rawPath string) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return
	}
	transport["path"] = path

	basePath, rawQuery, hasQuery := strings.Cut(path, "?")
	if !hasQuery || strings.TrimSpace(rawQuery) == "" {
		return
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) == 0 {
		return
	}

	var (
		edText string
		ehText string
	)
	for key, list := range values {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "ed":
			if edText == "" {
				for _, item := range list {
					if item = strings.TrimSpace(item); item != "" {
						edText = item
						break
					}
				}
			}
		case "eh":
			if ehText == "" {
				for _, item := range list {
					if item = strings.TrimSpace(item); item != "" {
						ehText = item
						break
					}
				}
			}
		default:
			// Keep literal path for unknown query keys to avoid changing semantics.
			return
		}
	}
	if edText == "" {
		return
	}

	maxEarlyData, err := strconv.ParseUint(edText, 10, 32)
	if err != nil || maxEarlyData == 0 {
		return
	}
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		basePath = "/"
	}
	transport["path"] = basePath
	transport["max_early_data"] = maxEarlyData
	if ehText == "" {
		ehText = "Sec-WebSocket-Protocol"
	}
	transport["early_data_header_name"] = ehText
}

func buildParsedNode(outbound map[string]any) (ParsedNode, bool) {
	raw, err := json.Marshal(outbound)
	if err != nil {
		return ParsedNode{}, false
	}
	var header outboundHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		return ParsedNode{}, false
	}
	if !supportedOutboundTypes[header.Type] {
		return ParsedNode{}, false
	}
	return ParsedNode{
		Tag:        header.Tag,
		RawOptions: json.RawMessage(raw),
	}, true
}

func normalizeInput(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	return bytes.TrimPrefix(trimmed, []byte{0xEF, 0xBB, 0xBF})
}

func normalizeTextContent(content string) string {
	content = strings.TrimPrefix(content, "\uFEFF")

	var b strings.Builder
	b.Grow(len(content))
	for _, r := range content {
		switch r {
		case '\u200B', '\u200C', '\u200D':
			continue
		}
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func getString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			return t
		case json.Number:
			return t.String()
		case int:
			return strconv.Itoa(t)
		case int8:
			return strconv.FormatInt(int64(t), 10)
		case int16:
			return strconv.FormatInt(int64(t), 10)
		case int32:
			return strconv.FormatInt(int64(t), 10)
		case int64:
			return strconv.FormatInt(t, 10)
		case uint:
			return strconv.FormatUint(uint64(t), 10)
		case uint8:
			return strconv.FormatUint(uint64(t), 10)
		case uint16:
			return strconv.FormatUint(uint64(t), 10)
		case uint32:
			return strconv.FormatUint(uint64(t), 10)
		case uint64:
			return strconv.FormatUint(t, 10)
		case float32:
			return strconv.FormatFloat(float64(t), 'f', -1, 64)
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(t)
		}
	}
	return ""
}

func getUint(m map[string]any, keys ...string) (uint64, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case int:
			if t >= 0 {
				return uint64(t), true
			}
		case int8:
			if t >= 0 {
				return uint64(t), true
			}
		case int16:
			if t >= 0 {
				return uint64(t), true
			}
		case int32:
			if t >= 0 {
				return uint64(t), true
			}
		case int64:
			if t >= 0 {
				return uint64(t), true
			}
		case uint:
			return uint64(t), true
		case uint8:
			return uint64(t), true
		case uint16:
			return uint64(t), true
		case uint32:
			return uint64(t), true
		case uint64:
			return t, true
		case float32:
			if t >= 0 {
				return uint64(t), true
			}
		case float64:
			if t >= 0 {
				return uint64(t), true
			}
		case string:
			parsed, err := strconv.ParseUint(strings.TrimSpace(t), 10, 64)
			if err == nil {
				return parsed, true
			}
		case json.Number:
			parsed, err := strconv.ParseUint(t.String(), 10, 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func getBool(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t, true
		case string:
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "1", "true", "yes", "on":
				return true, true
			case "0", "false", "no", "off":
				return false, true
			}
		}
	}
	return false, false
}

func getMap(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			return t, true
		case map[any]any:
			converted := make(map[string]any, len(t))
			for mk, mv := range t {
				converted[fmt.Sprint(mk)] = mv
			}
			return converted, true
		}
	}
	return nil, false
}

func getStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}

	switch t := v.(type) {
	case string:
		return splitALPN(t)
	case []string:
		var out []string
		for _, item := range t {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func getStringList(m map[string]any, keys ...string) []string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		if values := parseStringListValue(v); len(values) > 0 {
			return values
		}
	}
	return nil
}

func parseStringListValue(value any) []string {
	switch t := value.(type) {
	case string:
		return splitCommaList(t)
	case []string:
		out := make([]string, 0, len(t))
		for _, item := range t {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func getUint8Array(m map[string]any, keys ...string) ([]int, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case []any:
			values := make([]int, 0, len(t))
			for _, item := range t {
				uint8Value, ok := parseUint8(item)
				if !ok {
					values = nil
					break
				}
				values = append(values, uint8Value)
			}
			if len(values) > 0 {
				return values, true
			}
		case []int:
			values := make([]int, 0, len(t))
			valid := true
			for _, item := range t {
				if item < 0 || item > 255 {
					valid = false
					break
				}
				values = append(values, item)
			}
			if valid && len(values) > 0 {
				return values, true
			}
		}
	}
	return nil, false
}

func parseUint8(raw any) (int, bool) {
	switch t := raw.(type) {
	case int:
		if t >= 0 && t <= 255 {
			return t, true
		}
	case int8:
		if t >= 0 {
			return int(t), true
		}
	case int16:
		if t >= 0 && t <= 255 {
			return int(t), true
		}
	case int32:
		if t >= 0 && t <= 255 {
			return int(t), true
		}
	case int64:
		if t >= 0 && t <= 255 {
			return int(t), true
		}
	case uint:
		if t <= 255 {
			return int(t), true
		}
	case uint8:
		return int(t), true
	case uint16:
		if t <= 255 {
			return int(t), true
		}
	case uint32:
		if t <= 255 {
			return int(t), true
		}
	case uint64:
		if t <= 255 {
			return int(t), true
		}
	case float32:
		if t >= 0 && t <= 255 && float32(int(t)) == t {
			return int(t), true
		}
	case float64:
		if t >= 0 && t <= 255 && float64(int(t)) == t {
			return int(t), true
		}
	case json.Number:
		value, err := strconv.ParseInt(t.String(), 10, 64)
		if err == nil && value >= 0 && value <= 255 {
			return int(value), true
		}
	case string:
		value, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err == nil && value >= 0 && value <= 255 {
			return int(value), true
		}
	}
	return 0, false
}

func queryBool(values url.Values, keys ...string) bool {
	for _, key := range keys {
		value := strings.TrimSpace(values.Get(key))
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func splitALPN(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func decodeTag(fragment string) string {
	if fragment == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(fragment)
	if err != nil {
		return strings.TrimSpace(fragment)
	}
	return strings.TrimSpace(decoded)
}

func uriPortOrDefault(u *url.URL, fallback uint64) uint64 {
	port := strings.TrimSpace(u.Port())
	if port == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fallback
	}
	return parsed
}

func defaultTag(tag string, proto string, server string, port uint64) string {
	if trimmed := strings.TrimSpace(tag); trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-%s:%d", proto, server, port)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
