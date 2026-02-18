package proxy

import (
	"fmt"
	"strings"
)

// NormalizeRulePrefix canonicalizes an account-header rule prefix.
//
// Rules:
//   - Trim surrounding whitespace.
//   - Reject empty values and values containing '?'.
//   - Keep "*" as wildcard.
//   - Lowercase only the host part before the first '/'.
//   - Keep path part (if any) as-is.
func NormalizeRulePrefix(prefix string) (string, error) {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return "", fmt.Errorf("url_prefix: must be non-empty")
	}
	if strings.Contains(p, "?") {
		return "", fmt.Errorf("url_prefix: must not contain '?'")
	}
	if p == "*" {
		return p, nil
	}

	parts := strings.SplitN(p, "/", 2)
	host := parts[0]
	if host == "" {
		return "", fmt.Errorf("url_prefix: host must be non-empty")
	}
	host = strings.ToLower(host)

	if len(parts) == 1 {
		return host, nil
	}
	return host + "/" + parts[1], nil
}
