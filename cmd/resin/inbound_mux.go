package main

import (
	"net/http"
	"net/url"
	"strings"
)

func newInboundMux(proxyToken string, forward, reverse, apiHandler, tokenActionHandler http.Handler) http.Handler {
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
		if shouldRouteForwardProxy(r) {
			forward.ServeHTTP(w, r)
			return
		}
		if shouldRouteControlPlane(r) {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if shouldRouteTokenInheritLeaseAction(r, proxyToken) {
			tokenActionHandler.ServeHTTP(w, r)
			return
		}
		if shouldRouteReservedTokenAPI(r, proxyToken) {
			http.NotFound(w, r)
			return
		}
		reverse.ServeHTTP(w, r)
	})
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

func shouldRouteReservedTokenAPI(r *http.Request, proxyToken string) bool {
	if proxyToken == "" || r == nil {
		return false
	}
	segments := escapedPathSegments(r)
	if len(segments) < 2 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok || token != proxyToken {
		return false
	}
	second, ok := decodePathSegment(segments[1])
	if !ok {
		return false
	}
	return second == "api"
}

func shouldRouteTokenInheritLeaseAction(r *http.Request, proxyToken string) bool {
	if proxyToken == "" || r == nil {
		return false
	}
	segments := escapedPathSegments(r)
	if len(segments) != 6 {
		return false
	}
	token, ok := decodePathSegment(segments[0])
	if !ok || token != proxyToken {
		return false
	}
	apiSeg, ok := decodePathSegment(segments[1])
	if !ok || apiSeg != "api" {
		return false
	}
	versionSeg, ok := decodePathSegment(segments[2])
	if !ok || versionSeg != "v1" {
		return false
	}
	platformSeg, ok := decodePathSegment(segments[3])
	if !ok || strings.TrimSpace(platformSeg) == "" {
		return false
	}
	actionsSeg, ok := decodePathSegment(segments[4])
	if !ok || actionsSeg != "actions" {
		return false
	}
	actionName, ok := decodePathSegment(segments[5])
	if !ok || actionName != "inherit-lease" {
		return false
	}
	return true
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
