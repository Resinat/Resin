package proxy

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/resin-proxy/resin/internal/model"
)

// AccountRuleMatcher provides longest-prefix rule matching for account headers.
// ReverseProxy depends on this interface to allow runtime matcher swapping.
type AccountRuleMatcher interface {
	Match(host, path string) []string
}

// AccountMatcher performs longest-prefix matching on (host, path) to find
// the set of header names from which to extract an account identity.
//
// Rules are stored in a segment-based trie keyed by domain (lowercase) then
// path segments. The wildcard key "*" serves as a catch-all fallback.
type AccountMatcher struct {
	root     *matcherNode
	wildcard []string // headers for the "*" catch-all rule, if any
}

var _ AccountRuleMatcher = (*AccountMatcher)(nil)

type matcherNode struct {
	children map[string]*matcherNode
	headers  []string // non-nil when this node is a terminal rule
}

// BuildAccountMatcher constructs a matcher from persisted rules.
func BuildAccountMatcher(rules []model.AccountHeaderRule) *AccountMatcher {
	m := &AccountMatcher{root: &matcherNode{}}
	for _, r := range rules {
		var headers []string
		if err := json.Unmarshal([]byte(r.HeadersJSON), &headers); err != nil {
			continue
		}
		prefix := r.URLPrefix
		if prefix == "*" {
			m.wildcard = headers
			continue
		}
		segments := splitPrefix(prefix)
		m.insertSegments(segments, headers)
	}
	return m
}

// Match returns the header names for the longest-prefix rule matching
// the given host and path. Returns nil if no rule matches.
func (m *AccountMatcher) Match(host, path string) []string {
	host = normalizeMatchHost(host)

	segments := []string{host}
	if path != "" {
		// Strip query string — URL prefix rules never contain '?'.
		if qi := strings.IndexByte(path, '?'); qi >= 0 {
			path = path[:qi]
		}
		path = strings.TrimPrefix(path, "/")
		if path != "" {
			segments = append(segments, strings.Split(path, "/")...)
		}
	}

	cur := m.root
	var bestHeaders []string

	for _, seg := range segments {
		child, ok := cur.children[seg]
		if !ok {
			break
		}
		cur = child
		if cur.headers != nil {
			bestHeaders = cur.headers
		}
	}

	if bestHeaders != nil {
		return bestHeaders
	}
	return m.wildcard // may be nil
}

func normalizeMatchHost(host string) string {
	host = strings.ToLower(host)
	if host == "" {
		return host
	}
	// Strip port when host is in host:port or [ipv6]:port form.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		// Handle bracketed IPv6 literal without port.
		host = host[1 : len(host)-1]
	}
	// Canonicalise IP literal formatting (especially IPv6).
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String()
	}
	return host
}

// extractAccountFromHeaders extracts the account from the first non-empty
// header value in the given ordered list.
func extractAccountFromHeaders(r *http.Request, headers []string) string {
	for _, h := range headers {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return ""
}

// splitPrefix splits a URL prefix like "api.example.com/v1/users" into
// segments ["api.example.com", "v1", "users"]. The domain portion is
// lowercased for case-insensitive matching.
func splitPrefix(prefix string) []string {
	// Split on the first "/" to separate domain from path.
	parts := strings.SplitN(prefix, "/", 2)
	domain := strings.ToLower(parts[0])
	segments := []string{domain}
	if len(parts) > 1 && parts[1] != "" {
		segments = append(segments, strings.Split(parts[1], "/")...)
	}
	return segments
}

func (m *AccountMatcher) insertSegments(segments []string, headers []string) {
	cur := m.root
	for _, seg := range segments {
		if cur.children == nil {
			cur.children = make(map[string]*matcherNode)
		}
		child, ok := cur.children[seg]
		if !ok {
			child = &matcherNode{}
			cur.children[seg] = child
		}
		cur = child
	}
	cur.headers = headers
}
