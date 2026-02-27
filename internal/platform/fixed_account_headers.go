package platform

import (
	"fmt"
	"net/textproto"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// NormalizeFixedAccountHeaders parses newline-delimited header names,
// canonicalizes them, removes duplicates (case-insensitive), and returns:
// 1) normalized newline-delimited value, 2) ordered header slice.
func NormalizeFixedAccountHeaders(raw string) (string, []string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, nil
	}

	lines := strings.Split(raw, "\n")
	headers := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for i, line := range lines {
		name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(strings.TrimSuffix(line, "\r")))
		if name == "" {
			continue
		}
		if !httpguts.ValidHeaderFieldName(name) {
			return "", nil, fmt.Errorf("invalid HTTP header field name at line %d", i+1)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		headers = append(headers, name)
	}

	return strings.Join(headers, "\n"), headers, nil
}
