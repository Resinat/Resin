package proxy

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"

	"github.com/Resinat/Resin/internal/model"
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
	prefix   string   // original url_prefix for this terminal rule
}

type normalizedRule struct {
	normalizedPrefix string
	rawPrefix        string
	headers          []string
	headersKey       string
	updatedAtNs      int64
}

func shouldReplaceRule(next, current normalizedRule) bool {
	if next.updatedAtNs != current.updatedAtNs {
		return next.updatedAtNs > current.updatedAtNs
	}
	if next.rawPrefix != current.rawPrefix {
		return next.rawPrefix < current.rawPrefix
	}
	return next.headersKey < current.headersKey
}

func headersTieBreakKey(headers []string) string {
	return strings.Join(headers, "\x00")
}

// BuildAccountMatcher constructs a matcher from persisted rules.
func BuildAccountMatcher(rules []model.AccountHeaderRule) *AccountMatcher {
	m := &AccountMatcher{root: &matcherNode{}}
	winners := make(map[string]normalizedRule)
	for _, r := range rules {
		headers := append([]string(nil), r.Headers...)
		normPrefix, err := NormalizeRulePrefix(r.URLPrefix)
		if err != nil {
			continue
		}
		candidate := normalizedRule{
			normalizedPrefix: normPrefix,
			rawPrefix:        r.URLPrefix,
			headers:          headers,
			headersKey:       headersTieBreakKey(headers),
			updatedAtNs:      r.UpdatedAtNs,
		}
		if current, ok := winners[normPrefix]; !ok || shouldReplaceRule(candidate, current) {
			winners[normPrefix] = candidate
		}
	}

	keys := make([]string, 0, len(winners))
	for key := range winners {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		r := winners[key]
		if r.normalizedPrefix == "*" {
			m.wildcard = r.headers
			continue
		}
		segments := splitPrefix(r.normalizedPrefix)
		m.insertSegments(segments, r.normalizedPrefix, r.headers)
	}
	return m
}

// Match returns the header names for the longest-prefix rule matching
// the given host and path. Returns nil if no rule matches.
func (m *AccountMatcher) Match(host, path string) []string {
	_, headers := m.MatchWithPrefix(host, path)
	return headers
}

// MatchWithPrefix returns the matched url_prefix and its headers for the
// longest-prefix rule matching the given host/path. If no rule matches,
// it returns ("", nil). Wildcard fallback returns ("*", wildcardHeaders).
func (m *AccountMatcher) MatchWithPrefix(host, path string) (string, []string) {
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
	bestPrefix := ""
	var bestHeaders []string

	for _, seg := range segments {
		child, ok := cur.children[seg]
		if !ok {
			break
		}
		cur = child
		if cur.headers != nil {
			bestHeaders = cur.headers
			bestPrefix = cur.prefix
		}
	}

	if bestHeaders != nil {
		return bestPrefix, bestHeaders
	}
	if m.wildcard != nil {
		return "*", m.wildcard
	}
	return "", nil
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

func (m *AccountMatcher) insertSegments(segments []string, prefix string, headers []string) {
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
	cur.prefix = prefix
}
