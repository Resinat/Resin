package proxy

import "strings"

// parseV1PlatformAccountIdentity parses V1 identity segment:
// "Platform.Account" (preferred) or "Platform:Account".
func parseV1PlatformAccountIdentity(identity string) (string, string) {
	dot := strings.IndexByte(identity, '.')
	colon := strings.IndexByte(identity, ':')
	switch {
	case dot < 0 && colon < 0:
		return identity, ""
	case dot < 0:
		return identity[:colon], identity[colon+1:]
	case colon < 0:
		return identity[:dot], identity[dot+1:]
	case dot < colon:
		return identity[:dot], identity[dot+1:]
	default:
		return identity[:colon], identity[colon+1:]
	}
}

// parseForwardCredentialV1 parses V1 forward credential:
// "<Platform><delimiter><Account>:<TOKEN>" where delimiter is '.' or ':'.
// TOKEN is split using the right-most ':'.
func parseForwardCredentialV1(credential string) (token string, platform string, account string) {
	identity := credential
	if idx := strings.LastIndexByte(credential, ':'); idx >= 0 {
		identity = credential[:idx]
		token = credential[idx+1:]
	}
	platform, account = parseV1PlatformAccountIdentity(identity)
	return token, platform, account
}

// parseForwardCredentialV1WhenAuthDisabled parses optional identity when
// RESIN_AUTH_VERSION=V1 and RESIN_PROXY_TOKEN is empty.
func parseForwardCredentialV1WhenAuthDisabled(credential string) (platform string, account string) {
	lastColon := strings.LastIndexByte(credential, ':')
	if lastColon >= 0 {
		identity := credential[:lastColon]
		if strings.IndexByte(identity, '.') >= 0 {
			_, platform, account = parseForwardCredentialV1(credential)
			return platform, account
		}
	}
	if strings.Count(credential, ":") == 1 {
		return parseV1PlatformAccountIdentity(credential)
	}
	_, platform, account = parseForwardCredentialV1(credential)
	return platform, account
}
