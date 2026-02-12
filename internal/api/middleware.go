package api

import (
	"net/http"
	"strings"
)

// AuthMiddleware returns an http.Handler that validates the Bearer token
// in the Authorization header against the expected admin token.
// If validation fails, it returns 401 Unauthorized with a JSON error body.
func AuthMiddleware(adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing Authorization header")
			return
		}

		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid Authorization header format")
			return
		}

		token := auth[len(prefix):]
		if token != adminToken {
			WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid admin token")
			return
		}

		next.ServeHTTP(w, r)
	})
}
