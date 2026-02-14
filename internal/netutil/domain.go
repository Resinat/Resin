package netutil

import (
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ExtractDomain extracts the effective top-level-domain-plus-one (eTLD+1)
// from a target string that may be host:port, a URL, an IPv6 address, etc.
//
// Examples:
//
//	"www.google.co.uk:443" -> "google.co.uk"
//	"api.sina.com.cn"      -> "sina.com.cn"
//	"192.168.1.1:8080"     -> "192.168.1.1"
//	"localhost"            -> "localhost"
//	"[::1]:80"             -> "::1"
func ExtractDomain(target string) string {
	// If target is a URL, parse out the host first.
	if strings.Contains(target, "://") || strings.HasPrefix(target, "//") {
		if u, err := url.Parse(target); err == nil && u.Host != "" {
			target = u.Host
		}
	}

	host := target

	// Split off port. net.SplitHostPort handles both "host:port" and "[ipv6]:port".
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		// Handle bare bracketed IPv6 like "[::1]" -> "::1".
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
	}

	// Use the Public Suffix List to extract eTLD+1.
	// Returns error for IP addresses, localhost, or bare TLDs.
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}

	// Fallback: return host as-is (IP addresses, internal names, etc.).
	return host
}
