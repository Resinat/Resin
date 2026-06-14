package proxy

import (
	"net"
	"net/http"
	"strconv"
)

// freeAccountPrefix namespaces password-less ("free-mode") port accounts so
// they never collide with real business accounts and are recognizable in the
// lease panel.
const freeAccountPrefix = "__free_"

// freeAccountFromPort derives the sticky-lease account for a free-mode port.
// Each port maps to a distinct account, so each port pins its own sticky lease
// (and thus its own egress node/IP) within the bound platform.
func freeAccountFromPort(port int) string {
	return freeAccountPrefix + strconv.Itoa(port)
}

// freeAccountFromAddr derives the account from a local listen address
// (host:port). Falls back to the bare prefix when the port cannot be parsed.
func freeAccountFromAddr(local net.Addr) string {
	if local != nil {
		if _, portStr, err := net.SplitHostPort(local.String()); err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				return freeAccountFromPort(port)
			}
		}
	}
	return freeAccountPrefix
}

// freeAccountFromRequest derives the account for an HTTP forward-proxy request
// served on a free-mode port, using the connection's local address exposed via
// http.LocalAddrContextKey.
func freeAccountFromRequest(r *http.Request) string {
	if r != nil {
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			return freeAccountFromAddr(local)
		}
	}
	return freeAccountPrefix
}
