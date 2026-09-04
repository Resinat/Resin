package api

import (
	"net/http"
	"time"

	"github.com/Resinat/Resin/internal/service"
)

// HandleExportData returns a handler for GET /api/v1/data/export.
func HandleExportData(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := cp.ExportData()
		if err != nil {
			writeServiceError(w, err)
			return
		}

		filename := "resin-export-" + time.Now().UTC().Format("20060102-150405") + ".json"
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		WriteJSON(w, http.StatusOK, payload)
	}
}

// HandleImportData returns a handler for POST /api/v1/data/import.
func HandleImportData(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload service.ExportPayload
		if err := DecodeBody(r, &payload); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		strategy := r.URL.Query().Get("strategy")
		result, err := cp.ImportData(payload, strategy)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}
