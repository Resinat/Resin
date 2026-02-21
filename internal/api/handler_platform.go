package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/service"
)

func platformMatchesKeyword(p service.PlatformResponse, keyword string) bool {
	contains := func(v string) bool {
		return strings.Contains(strings.ToLower(v), keyword)
	}

	if contains(p.ID) || contains(p.Name) {
		return true
	}
	for _, item := range p.RegionFilters {
		if contains(item) {
			return true
		}
	}
	return false
}

func filterPlatformsByKeyword(platforms []service.PlatformResponse, rawKeyword string) []service.PlatformResponse {
	keyword := strings.ToLower(strings.TrimSpace(rawKeyword))
	if keyword == "" {
		return platforms
	}
	filtered := make([]service.PlatformResponse, 0, len(platforms))
	for _, p := range platforms {
		if platformMatchesKeyword(p, keyword) {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

func platformSortKey(sortBy string, p service.PlatformResponse) string {
	switch sortBy {
	case "id":
		return p.ID
	case "updated_at":
		return p.UpdatedAt
	default:
		return p.Name
	}
}

func comparePlatformsForList(a, b service.PlatformResponse, sorting Sorting) int {
	aBuiltin := a.ID == platform.DefaultPlatformID
	bBuiltin := b.ID == platform.DefaultPlatformID
	if aBuiltin != bBuiltin {
		if aBuiltin {
			return -1
		}
		return 1
	}

	primary := strings.Compare(platformSortKey(sorting.SortBy, a), platformSortKey(sorting.SortBy, b))
	if sorting.SortOrder == "desc" {
		primary = -primary
	}
	if primary != 0 {
		return primary
	}
	// keep stable deterministic output when primary sort key is equal
	return strings.Compare(a.ID, b.ID)
}

// HandleListPlatforms returns a handler for GET /api/v1/platforms.
func HandleListPlatforms(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platforms, err := cp.ListPlatforms()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		platforms = filterPlatformsByKeyword(platforms, r.URL.Query().Get("keyword"))

		sorting, ok := parseSortingOrWriteInvalid(w, r, []string{"name", "id", "updated_at"}, "name", "asc")
		if !ok {
			return
		}
		slices.SortStableFunc(platforms, func(a, b service.PlatformResponse) int {
			return comparePlatformsForList(a, b, sorting)
		})

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, platforms, pg)
	}
}

// HandleGetPlatform returns a handler for GET /api/v1/platforms/{id}.
func HandleGetPlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}

		p, err := cp.GetPlatform(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, p)
	}
}

// HandleCreatePlatform returns a handler for POST /api/v1/platforms.
func HandleCreatePlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.CreatePlatformRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		p, err := cp.CreatePlatform(req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, p)
	}
}

// HandleUpdatePlatform returns a handler for PATCH /api/v1/platforms/{id}.
func HandleUpdatePlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}

		body, ok := readRawBodyOrWriteInvalid(w, r)
		if !ok {
			return
		}
		p, err := cp.UpdatePlatform(id, body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, p)
	}
}

// HandleDeletePlatform returns a handler for DELETE /api/v1/platforms/{id}.
func HandleDeletePlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		if err := cp.DeletePlatform(id); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleResetPlatform returns a handler for POST /api/v1/platforms/{id}/actions/reset-to-default.
func HandleResetPlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		p, err := cp.ResetPlatformToDefault(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, p)
	}
}

// HandleRebuildPlatform returns a handler for POST /api/v1/platforms/{id}/actions/rebuild-routable-view.
func HandleRebuildPlatform(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		if err := cp.RebuildPlatformView(id); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandlePreviewFilter returns a handler for POST /api/v1/platforms/preview-filter.
func HandlePreviewFilter(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.PreviewFilterRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if req.PlatformID != nil && *req.PlatformID != "" && !ValidateUUID(*req.PlatformID) {
			writeInvalidArgument(w, "platform_id: must be a valid UUID")
			return
		}
		nodes, err := cp.PreviewFilter(req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, nodes, pg)
	}
}
