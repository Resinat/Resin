package api

import (
	"net/http"
	"sync/atomic"

	"github.com/resin-proxy/resin/internal/config"
	"github.com/resin-proxy/resin/internal/service"
)

// HandleSystemInfo returns a handler for GET /api/v1/system/info.
func HandleSystemInfo(info service.SystemInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, info)
	}
}

// HandleSystemConfig returns a handler for GET /api/v1/system/config.
func HandleSystemConfig(runtimeCfg *atomic.Pointer[config.RuntimeConfig]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtimeCfg == nil {
			WriteJSON(w, http.StatusOK, nil)
			return
		}
		WriteJSON(w, http.StatusOK, runtimeCfg.Load())
	}
}

// HandleSystemDefaultConfig returns a handler for GET /api/v1/system/config/default.
func HandleSystemDefaultConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, config.NewDefaultRuntimeConfig())
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
