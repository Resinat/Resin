package api

import "net/http"

// HandleHealthz returns a handler for GET /healthz.
// No authentication is required.
func HandleHealthz() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
