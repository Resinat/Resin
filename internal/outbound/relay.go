package outbound

import (
	"encoding/json"
	"slices"

	"github.com/sagernet/sing-box/adapter"
)

// relayNetworks describes forwarding capability, rather than the transport
// used by the relay to reach its own server. Unknown types are not relays.
func relayNetworks(raw json.RawMessage, ob adapter.Outbound) []string {
	var cfg struct {
		Type       string          `json:"type"`
		Version    string          `json:"version"`
		UDPOverTCP json.RawMessage `json:"udp_over_tcp"`
		Network    json.RawMessage `json:"network"`
	}
	if json.Unmarshal(raw, &cfg) != nil || ob == nil {
		return nil
	}
	switch cfg.Type {
	case "tor", "http", "ssh", "socks", "shadowsocks", "vmess", "vless", "trojan", "anytls", "hysteria", "hysteria2", "tuic", "wireguard":
	default:
		// ShadowTLS is a transport and does not forward arbitrary destinations.
		return nil
	}
	networks := slices.Clone(ob.Network())
	if cfg.Type == "tor" || cfg.Type == "http" || cfg.Type == "ssh" {
		networks = slices.DeleteFunc(networks, func(n string) bool { return n != "tcp" })
	}
	if len(cfg.Network) > 0 {
		var allowed []string
		if json.Unmarshal(cfg.Network, &allowed) != nil {
			var value string
			if json.Unmarshal(cfg.Network, &value) == nil {
				allowed = []string{value}
			}
		}
		if len(allowed) > 0 {
			networks = slices.DeleteFunc(networks, func(n string) bool { return !slices.Contains(allowed, n) })
		}
	}
	if cfg.Type == "socks" && (cfg.Version == "4" || cfg.Version == "4a") {
		var uot struct {
			Enabled bool `json:"enabled"`
		}
		enabled := string(cfg.UDPOverTCP) == "true"
		if json.Unmarshal(cfg.UDPOverTCP, &uot) == nil {
			enabled = enabled || uot.Enabled
		}
		if !enabled {
			networks = slices.DeleteFunc(networks, func(n string) bool { return n == "udp" })
		}
	}
	return networks
}
