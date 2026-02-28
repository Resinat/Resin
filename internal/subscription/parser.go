package subscription

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
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
// subscriptions, plus plain HTTP proxy lines (IP:PORT or IP:PORT:USER:PASS),
// with optional base64-wrapped content support.
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
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return nil, true, fmt.Errorf("subscription: unmarshal clash yaml: %w", err)
	}
	return parseClashProxies(cfg.Proxies), true, nil
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
		method := strings.TrimSpace(firstNonEmpty(getString(proxy, "cipher"), getString(proxy, "method")))
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
		setWSTransportFromClash(outbound, proxy)
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
		if flow := strings.TrimSpace(getString(proxy, "flow")); flow != "" {
			outbound["flow"] = flow
		}
		setTLSFromClash(outbound, proxy, "tls")
		setWSTransportFromClash(outbound, proxy)
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
		setWSTransportFromClash(outbound, proxy)
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
		outbound := map[string]any{
			"type":        "hysteria2",
			"tag":         defaultTag(tag, "hysteria2", server, port),
			"server":      server,
			"server_port": port,
			"password":    password,
			"tls":         tls,
		}
		return buildParsedNode(outbound)
	default:
		return ParsedNode{}, false
	}
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
			node ParsedNode
			ok   bool
		)
		switch {
		case strings.HasPrefix(lower, "vmess://"):
			recognized = true
			node, ok = parseVmessURI(line)
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
		default:
			node, ok = parsePlainHTTPProxyLine(line)
			if ok {
				recognized = true
			}
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

func parseVmessURI(uri string) (ParsedNode, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(uri, "vmess://"))
	if payload == "" {
		return ParsedNode{}, false
	}

	decoded, ok := decodeBase64Relaxed(payload)
	if !ok || !utf8.Valid(decoded) {
		return ParsedNode{}, false
	}

	var v map[string]any
	if err := json.Unmarshal(decoded, &v); err != nil {
		return ParsedNode{}, false
	}

	server := strings.TrimSpace(getString(v, "add"))
	uuid := strings.TrimSpace(getString(v, "id"))
	if server == "" || uuid == "" {
		return ParsedNode{}, false
	}

	port := uint64(443)
	if parsedPort, ok := getUint(v, "port"); ok {
		port = parsedPort
	}
	tag := strings.TrimSpace(getString(v, "ps"))
	if tag == "" {
		tag = fmt.Sprintf("vmess-%s:%d", server, port)
	}
	security := strings.TrimSpace(getString(v, "scy", "security"))
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
	if tlsValue == "tls" || tlsValue == "1" || tlsValue == "true" {
		tls := map[string]any{"enabled": true}
		if sni := strings.TrimSpace(firstNonEmpty(getString(v, "sni"), getString(v, "host"))); sni != "" {
			tls["server_name"] = sni
		}
		outbound["tls"] = tls
	}

	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		getString(v, "net"),
		getString(v, "type"),
		getString(v, "network"),
	)))
	if network == "ws" {
		transport := map[string]any{"type": "ws"}
		if path := strings.TrimSpace(getString(v, "path")); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(getString(v, "host")); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		outbound["transport"] = transport
	}

	return buildParsedNode(outbound)
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
	if flow := strings.TrimSpace(query.Get("flow")); flow != "" {
		outbound["flow"] = flow
	}

	security := strings.ToLower(strings.TrimSpace(query.Get("security")))
	sni := strings.TrimSpace(firstNonEmpty(query.Get("sni"), query.Get("servername")))
	if security == "tls" || security == "reality" || sni != "" {
		tls := map[string]any{"enabled": true}
		if sni != "" {
			tls["server_name"] = sni
		}
		outbound["tls"] = tls
	}

	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("network"))))
	if network == "ws" {
		transport := map[string]any{"type": "ws"}
		if path := strings.TrimSpace(query.Get("path")); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(query.Get("host")); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		outbound["transport"] = transport
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

	tls := map[string]any{
		"enabled":     true,
		"server_name": serverName,
	}
	if insecure {
		tls["insecure"] = true
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
	if network == "ws" {
		transport := map[string]any{"type": "ws"}
		if path := strings.TrimSpace(query.Get("path")); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(query.Get("host")); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		outbound["transport"] = transport
	}

	return buildParsedNode(outbound)
}

func parseHysteria2URI(uri string) (ParsedNode, bool) {
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
		tag = defaultTag("", "hysteria2", server, port)
	}

	query := u.Query()
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

	outbound := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"password":    password,
		"tls":         tls,
	}
	return buildParsedNode(outbound)
}

func parseSSURI(uri string) (ParsedNode, bool) {
	raw := strings.TrimSpace(strings.TrimPrefix(uri, "ss://"))
	if raw == "" {
		return ParsedNode{}, false
	}

	beforeFragment, fragment, _ := strings.Cut(raw, "#")
	beforeQuery, _, _ := strings.Cut(beforeFragment, "?")
	tag := decodeTag(fragment)
	if tag == "" {
		tag = "shadowsocks"
	}

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
			"tag":         tag,
			"server":      server,
			"server_port": port,
			"method":      method,
			"password":    password,
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
		"tag":         tag,
		"server":      server,
		"server_port": port,
		"method":      method,
		"password":    password,
	}
	return buildParsedNode(outbound)
}

func parseSSMethodPassword(input string) (string, string, bool) {
	if method, password, ok := strings.Cut(input, ":"); ok {
		method = strings.TrimSpace(method)
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
	method = strings.TrimSpace(method)
	password = strings.TrimSpace(password)
	if method == "" || password == "" {
		return "", "", false
	}
	return method, password, true
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
	outbound["tls"] = tls
}

func setWSTransportFromClash(outbound map[string]any, proxy map[string]any) {
	if strings.ToLower(strings.TrimSpace(getString(proxy, "network"))) != "ws" {
		return
	}
	transport := map[string]any{"type": "ws"}
	if wsOpts, ok := getMap(proxy, "ws-opts", "ws_opts"); ok {
		if path := strings.TrimSpace(getString(wsOpts, "path")); path != "" {
			transport["path"] = path
		}
		if headers, ok := getMap(wsOpts, "headers"); ok && len(headers) > 0 {
			transport["headers"] = headers
		}
	}
	outbound["transport"] = transport
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
