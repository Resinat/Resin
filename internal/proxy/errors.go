// Package proxy implements the forward and reverse proxy data plane for Resin.
package proxy

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/Resinat/Resin/internal/routing"
)

// ProxyError represents a structured proxy error response.
type ProxyError struct {
	HTTPCode   int
	ResinError string // X-Resin-Error header value
	Message    string // plain-text body
}

// Predefined proxy errors aligned with DESIGN.md error specification.
var (
	ErrAuthRequired = &ProxyError{
		HTTPCode:   http.StatusProxyAuthRequired,
		ResinError: "AUTH_REQUIRED",
		Message:    "Proxy authentication required",
	}
	ErrAuthFailed = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		ResinError: "AUTH_FAILED",
		Message:    "Proxy authentication failed",
	}
	ErrURLParseError = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		ResinError: "URL_PARSE_ERROR",
		Message:    "Failed to parse request URL",
	}
	ErrInvalidProtocol = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		ResinError: "INVALID_PROTOCOL",
		Message:    "Protocol must be http or https",
	}
	ErrInvalidHost = &ProxyError{
		HTTPCode:   http.StatusBadRequest,
		ResinError: "INVALID_HOST",
		Message:    "Invalid or empty host",
	}
	ErrPlatformNotFound = &ProxyError{
		HTTPCode:   http.StatusNotFound,
		ResinError: "PLATFORM_NOT_FOUND",
		Message:    "Platform not found",
	}
	ErrAccountRejected = &ProxyError{
		HTTPCode:   http.StatusForbidden,
		ResinError: "ACCOUNT_REJECTED",
		Message:    "Account extraction failed and platform rejects unmatched requests",
	}
	ErrNoAvailableNodes = &ProxyError{
		HTTPCode:   http.StatusServiceUnavailable,
		ResinError: "NO_AVAILABLE_NODES",
		Message:    "No available nodes for routing",
	}
	ErrUpstreamConnectFailed = &ProxyError{
		HTTPCode:   http.StatusBadGateway,
		ResinError: "UPSTREAM_CONNECT_FAILED",
		Message:    "Failed to connect to upstream",
	}
	ErrUpstreamTimeout = &ProxyError{
		HTTPCode:   http.StatusGatewayTimeout,
		ResinError: "UPSTREAM_TIMEOUT",
		Message:    "Upstream connection or response timed out",
	}
	ErrUpstreamRequestFailed = &ProxyError{
		HTTPCode:   http.StatusBadGateway,
		ResinError: "UPSTREAM_REQUEST_FAILED",
		Message:    "Upstream request failed",
	}
	ErrInternalError = &ProxyError{
		HTTPCode:   http.StatusInternalServerError,
		ResinError: "INTERNAL_ERROR",
		Message:    "Internal proxy error",
	}
)

// writeProxyError writes a standardised proxy error response.
// For 407 responses, the Proxy-Authenticate header is added automatically.
func writeProxyError(w http.ResponseWriter, pe *ProxyError) {
	if pe.HTTPCode == http.StatusProxyAuthRequired {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Resin"`)
	}
	w.Header().Set("X-Resin-Error", pe.ResinError)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(pe.HTTPCode)
	w.Write([]byte(pe.Message))
}

// classifyUpstreamError maps an upstream error to the appropriate ProxyError.
// Used for forward HTTP and reverse proxy paths (NOT CONNECT).
// Returns nil for context.Canceled (client-initiated cancellation should not
// be treated as a node health failure).
func classifyUpstreamError(err error) *ProxyError {
	if err == nil {
		return nil
	}
	// Client-initiated cancel — not a node failure.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	// Timeout (context deadline or OS-level).
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamTimeout
	}
	// Everything else (dial failure, read/write errors, connection reset, etc.).
	// In non-CONNECT paths, all upstream failures are UPSTREAM_REQUEST_FAILED.
	return ErrUpstreamRequestFailed
}

// classifyConnectError classifies errors in the CONNECT dial path.
// In CONNECT, all errors are dial-phase errors, so non-timeout/non-canceled
// errors always map to UPSTREAM_CONNECT_FAILED (not UPSTREAM_REQUEST_FAILED).
func classifyConnectError(err error) *ProxyError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUpstreamTimeout
	}
	return ErrUpstreamConnectFailed
}

// mapRouteError translates a routing-layer error into a ProxyError.
func mapRouteError(err error) *ProxyError {
	if errors.Is(err, routing.ErrPlatformNotFound) {
		return ErrPlatformNotFound
	}
	if errors.Is(err, routing.ErrNoAvailableNodes) {
		return ErrNoAvailableNodes
	}
	return ErrInternalError
}
