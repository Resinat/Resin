package proxy

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// TargetBypassMatcher decides whether a target should skip Resin node routing
// and be dialed directly from the Resin process.
type TargetBypassMatcher struct {
	rules []targetBypassRule
}

type targetBypassRule struct {
	local bool
	cidr  netip.Prefix
	exact string
	glob  *regexp.Regexp
}

func NewTargetBypassMatcher(patterns []string) *TargetBypassMatcher {
	m := &TargetBypassMatcher{}
	for _, pattern := range patterns {
		if rule, ok := compileTargetBypassRule(pattern); ok {
			m.rules = append(m.rules, rule)
		}
	}
	if len(m.rules) == 0 {
		return nil
	}
	return m
}

func (m *TargetBypassMatcher) ShouldBypass(target string) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	host := targetHostForBypass(target)
	if host == "" {
		return false
	}
	host = strings.ToLower(host)
	addr, hasAddr := parseAddrForBypass(host)
	for _, rule := range m.rules {
		switch {
		case rule.local:
			if isLocalBypassHost(host) {
				return true
			}
		case rule.cidr.IsValid():
			if hasAddr && rule.cidr.Contains(addr) {
				return true
			}
		case rule.glob != nil:
			if rule.glob.MatchString(host) {
				return true
			}
		case rule.exact != "":
			if host == rule.exact {
				return true
			}
		}
	}
	return false
}

func compileTargetBypassRule(raw string) (targetBypassRule, bool) {
	pattern := strings.TrimSpace(raw)
	if pattern == "" {
		return targetBypassRule{}, false
	}
	if strings.EqualFold(pattern, "<local>") {
		return targetBypassRule{local: true}, true
	}
	if prefix, err := netip.ParsePrefix(stripIPv6Zone(strings.Trim(pattern, "[]"))); err == nil {
		return targetBypassRule{cidr: prefix}, true
	}

	pattern = strings.ToLower(targetHostForBypass(pattern))
	if pattern == "" {
		return targetBypassRule{}, false
	}
	if strings.ContainsAny(pattern, "*?") {
		return targetBypassRule{glob: compileBypassGlob(pattern)}, true
	}
	return targetBypassRule{exact: pattern}, true
}

func compileBypassGlob(pattern string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, ".*")
	quoted = strings.ReplaceAll(quoted, `\?`, ".")
	return regexp.MustCompile("^" + quoted + "$")
}

func targetHostForBypass(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.Contains(target, "://") || strings.HasPrefix(target, "//") {
		if u, err := url.Parse(target); err == nil && u.Host != "" {
			target = u.Host
		}
	}
	if host, _, err := net.SplitHostPort(target); err == nil {
		return strings.Trim(host, "[]")
	}
	if strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]") {
		return strings.Trim(target, "[]")
	}
	if i := strings.LastIndexByte(target, ':'); i > 0 && strings.Count(target, ":") == 1 {
		if _, err := strconv.Atoi(target[i+1:]); err == nil {
			return strings.Trim(target[:i], "[]")
		}
	}
	return strings.Trim(target, "[]")
}

func parseAddrForBypass(host string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(stripIPv6Zone(host))
	return addr, err == nil
}

func stripIPv6Zone(host string) string {
	if i := strings.LastIndexByte(host, '%'); i >= 0 {
		return host[:i]
	}
	return host
}

func isLocalBypassHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if _, ok := parseAddrForBypass(host); ok {
		return false
	}
	return !strings.Contains(host, ".")
}
