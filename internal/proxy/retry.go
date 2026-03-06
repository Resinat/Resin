package proxy

// isRetryableUpstreamError returns true if the proxy error is a transient
// upstream failure that may succeed on a different node.
func isRetryableUpstreamError(pe *ProxyError) bool {
	if pe == nil {
		return false
	}
	switch pe {
	case ErrUpstreamConnectFailed, ErrUpstreamTimeout, ErrUpstreamRequestFailed:
		return true
	default:
		return false
	}
}

// platformMaxRetries returns MaxRetries for the named platform, or 0 if
// the platform is unknown or lookup is nil.
func platformMaxRetries(look PlatformLookup, platName string) int {
	if look == nil || platName == "" {
		return 0
	}
	plat, ok := look.GetPlatformByName(platName)
	if !ok || plat == nil {
		return 0
	}
	return plat.MaxRetries
}
