package subscription

import (
	"encoding/json"
	"fmt"
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

// ParseSingboxSubscription parses a sing-box subscription response and returns
// the list of supported outbound nodes. Individual outbound entries that fail
// to unmarshal are skipped, not treated as a fatal error.
func ParseSingboxSubscription(data []byte) ([]ParsedNode, error) {
	var resp subscriptionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("subscription: unmarshal response: %w", err)
	}

	var nodes []ParsedNode
	for _, raw := range resp.Outbounds {
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
			RawOptions: json.RawMessage(append([]byte(nil), raw...)), // defensive copy
		})
	}

	return nodes, nil
}
