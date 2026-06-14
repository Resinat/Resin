package netutil

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Access modes for free-mode (password-less) ports.
const (
	// AccessModeIntranet allows only private / loopback / link-local clients.
	AccessModeIntranet = "intranet"
	// AccessModeWhitelist allows only clients matching a configured IP/CIDR list.
	AccessModeWhitelist = "whitelist"
)

// AccessController decides whether a remote address may use a free-mode port.
//
// It is the single source of truth for both config validation (constructing it
// validates the inputs) and the runtime connection gate.
type AccessController struct {
	mode     string
	prefixes []netip.Prefix
}

// NewAccessController builds an AccessController and validates its inputs.
//   - mode must be AccessModeIntranet or AccessModeWhitelist.
//   - In whitelist mode the list must be non-empty and every entry must be a
//     valid IP address or CIDR prefix.
func NewAccessController(mode string, whitelist []string) (*AccessController, error) {
	switch mode {
	case AccessModeIntranet:
		return &AccessController{mode: mode}, nil
	case AccessModeWhitelist:
		if len(whitelist) == 0 {
			return nil, fmt.Errorf("whitelist must be non-empty in %q mode", AccessModeWhitelist)
		}
		prefixes := make([]netip.Prefix, 0, len(whitelist))
		for _, raw := range whitelist {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				return nil, fmt.Errorf("whitelist entry must not be empty")
			}
			prefix, err := parsePrefixOrAddr(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid IP/CIDR %q: %w", entry, err)
			}
			prefixes = append(prefixes, prefix)
		}
		return &AccessController{mode: mode, prefixes: prefixes}, nil
	default:
		return nil, fmt.Errorf(
			"invalid access mode %q (allowed: %s, %s)",
			mode, AccessModeIntranet, AccessModeWhitelist,
		)
	}
}

// Allow reports whether the remote address may use the port.
// A nil controller or unparseable address denies by default (fail-closed).
func (a *AccessController) Allow(remote net.Addr) bool {
	if a == nil {
		return false
	}
	addr, ok := addrToNetip(remote)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	switch a.mode {
	case AccessModeIntranet:
		return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast()
	case AccessModeWhitelist:
		for _, prefix := range a.prefixes {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func parsePrefixOrAddr(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, err
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func addrToNetip(remote net.Addr) (netip.Addr, bool) {
	if remote == nil {
		return netip.Addr{}, false
	}
	if tcp, ok := remote.(*net.TCPAddr); ok {
		if a, ok := netip.AddrFromSlice(tcp.IP); ok {
			return a, true
		}
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		host = remote.String()
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a, true
}
