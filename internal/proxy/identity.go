package proxy

import "strings"

// parsePlatformAccount parses "Platform:Account" and splits on the first colon.
// If no colon exists, the whole string is treated as platform name.
func parsePlatformAccount(identity string) (string, string) {
	if idx := strings.IndexByte(identity, ':'); idx >= 0 {
		return identity[:idx], identity[idx+1:]
	}
	return identity, ""
}
