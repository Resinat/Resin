package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/proxy"
)

func newEndpointInboundMux(
	currentEndpoint func() model.Endpoint,
	proxyToken string,
	forward, reverse, apiHandler, tokenActionHandler http.Handler,
) http.Handler {
	if forward == nil {
		forward = http.NotFoundHandler()
	}
	if reverse == nil {
		reverse = http.NotFoundHandler()
	}
	if apiHandler == nil {
		apiHandler = http.NotFoundHandler()
	}
	if tokenActionHandler == nil {
		tokenActionHandler = http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := model.Endpoint{}
		if currentEndpoint != nil {
			endpoint = currentEndpoint()
		}
		if shouldRouteForwardProxy(r) {
			if !endpoint.AllowProxy || !endpoint.AllowHTTPForward {
				writeEndpointCapabilityDisabled(w)
				return
			}
			r = r.WithContext(proxy.ContextWithInboundPolicy(r.Context(), proxy.InboundPolicy{
				RequireProxyAuthInfo: endpoint.RequireProxyAuthInfo,
			}))
			forward.ServeHTTP(w, r)
			return
		}
		if shouldRouteControlPlane(r) {
			if r.URL != nil && r.URL.Path != "/healthz" && !endpoint.AllowManagement {
				http.NotFound(w, r)
				return
			}
			apiHandler.ServeHTTP(w, r)
			return
		}
		if shouldRouteTokenAPI(r, proxyToken) {
			if !endpoint.AllowProxy {
				writeEndpointCapabilityDisabled(w)
				return
			}
			tokenActionHandler.ServeHTTP(w, r)
			return
		}
		if !endpoint.AllowProxy || !endpoint.AllowHTTPReverse {
			writeEndpointCapabilityDisabled(w)
			return
		}
		if shouldRejectReverseProxyByToken(r, proxyToken) {
			writeInboundAuthFailed(w)
			return
		}
		reverse.ServeHTTP(w, r)
	})
}

func writeEndpointCapabilityDisabled(w http.ResponseWriter) {
	w.Header().Set("X-Resin-Error", "ENDPOINT_CAPABILITY_DISABLED")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Proxy capability is disabled on this endpoint"))
}

func shouldRouteForwardProxy(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Method == http.MethodConnect {
		return true
	}
	if r.URL != nil && r.URL.IsAbs() {
		return true
	}
	uri := strings.ToLower(strings.TrimSpace(r.RequestURI))
	return strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://")
}

func shouldRouteTokenAPI(r *http.Request, proxyToken string) bool {
	if r == nil {
		return false
	}
	segments := escapedPathSegments(r)
	if len(segments) < 2 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok {
		return false
	}
	if proxyToken != "" && token != proxyToken {
		return false
	}
	apiSeg, ok := decodePathSegment(segments[1])
	return ok && apiSeg == "api"
}

func shouldRejectReverseProxyByToken(r *http.Request, proxyToken string) bool {
	if proxyToken == "" || r == nil {
		return false
	}
	segments := escapedPathSegments(r)
	if len(segments) == 0 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok {
		// Keep malformed percent-encoding behavior in reverse parser.
		return false
	}
	return token != proxyToken
}

func writeInboundAuthFailed(w http.ResponseWriter) {
	w.Header().Set("X-Resin-Error", "AUTH_FAILED")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("Proxy authentication failed"))
}

func shouldRouteControlPlane(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.URL == nil {
		return false
	}
	switch p := r.URL.Path; {
	case p == "/":
		return true
	case p == "/healthz":
		return true
	case p == "/api" || strings.HasPrefix(p, "/api/"):
		return true
	case p == "/ui" || strings.HasPrefix(p, "/ui/"):
		return true
	default:
		return false
	}
}

func escapedPathSegments(r *http.Request) []string {
	if r == nil || r.URL == nil {
		return nil
	}
	rawPath := r.URL.EscapedPath()
	if rawPath == "" {
		rawPath = r.URL.Path
	}
	path := strings.TrimPrefix(rawPath, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func decodePathSegment(segment string) (string, bool) {
	decoded, err := url.PathUnescape(segment)
	if err != nil {
		return "", false
	}
	return decoded, true
}
