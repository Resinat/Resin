package api

import (
	"net/http"

	"github.com/resin-proxy/resin/internal/service"
)

// HandleSystemInfo returns a handler for GET /api/v1/system/info.
func HandleSystemInfo(svc service.SystemService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, svc.GetSystemInfo())
	}
}

// HandleSystemConfig returns a handler for GET /api/v1/system/config.
func HandleSystemConfig(svc service.SystemService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, svc.GetRuntimeConfig())
	}
}

// HandlePatchSystemConfig returns a handler for PATCH /api/v1/system/config.
func HandlePatchSystemConfig(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRawBodyOrWriteInvalid(w, r)
		if !ok {
			return
		}
		result, err := cp.PatchRuntimeConfig(body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}
