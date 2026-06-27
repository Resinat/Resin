package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/Resinat/Resin/internal/service"
)

type subscriptionListSummary struct {
	EnabledCount        int   `json:"enabled_count"`
	DisabledCount       int   `json:"disabled_count"`
	UsageUsedBytes      int64 `json:"usage_used_bytes"`
	UsageTotalBytes     int64 `json:"usage_total_bytes"`
	UsageRemainingBytes int64 `json:"usage_remaining_bytes"`
	HealthyNodeCount    int   `json:"healthy_node_count"`
	NodeCount           int   `json:"node_count"`
}

type subscriptionListPageResponse struct {
	Items   []service.SubscriptionResponse `json:"items"`
	Total   int                            `json:"total"`
	Limit   int                            `json:"limit"`
	Offset  int                            `json:"offset"`
	Summary subscriptionListSummary        `json:"summary"`
}

func subscriptionMatchesKeyword(s service.SubscriptionResponse, keyword string) bool {
	contains := func(v string) bool {
		return strings.Contains(strings.ToLower(v), keyword)
	}

	return contains(s.ID) || contains(s.Name) || contains(s.URL) || contains(s.SourceType)
}

func filterSubscriptionsByKeyword(subs []service.SubscriptionResponse, rawKeyword string) []service.SubscriptionResponse {
	keyword := strings.ToLower(strings.TrimSpace(rawKeyword))
	if keyword == "" {
		return subs
	}
	filtered := make([]service.SubscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		if subscriptionMatchesKeyword(sub, keyword) {
			filtered = append(filtered, sub)
		}
	}
	return filtered
}

func filterSubscriptionsByEnabled(subs []service.SubscriptionResponse, enabled *bool) []service.SubscriptionResponse {
	if enabled == nil {
		return subs
	}
	filtered := make([]service.SubscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		if sub.Enabled == *enabled {
			filtered = append(filtered, sub)
		}
	}
	return filtered
}

func subscriptionSortKey(sortBy string, s service.SubscriptionResponse) string {
	switch sortBy {
	case "created_at":
		return s.CreatedAt
	case "last_checked":
		return s.LastChecked
	case "last_updated":
		return s.LastUpdated
	default:
		return s.Name
	}
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func summarizeSubscriptions(subs []service.SubscriptionResponse) subscriptionListSummary {
	var summary subscriptionListSummary
	for _, sub := range subs {
		if sub.Enabled {
			summary.EnabledCount++
		} else {
			summary.DisabledCount++
			continue
		}
		summary.HealthyNodeCount += sub.HealthyNodeCount
		summary.NodeCount += sub.NodeCount
		if sub.Usage == nil {
			continue
		}
		usedBytes := nonNegativeInt64(sub.Usage.UploadBytes) + nonNegativeInt64(sub.Usage.DownloadBytes)
		summary.UsageUsedBytes += usedBytes
		if sub.Usage.TotalBytes > 0 {
			summary.UsageTotalBytes += sub.Usage.TotalBytes
			remaining := sub.Usage.TotalBytes - usedBytes
			if remaining > 0 {
				summary.UsageRemainingBytes += remaining
			}
		}
	}
	return summary
}

func compareSubscriptionsForList(a, b service.SubscriptionResponse, sorting Sorting) int {
	if sorting.SortBy == "status" {
		if a.Enabled != b.Enabled {
			order := 1
			if a.Enabled {
				order = -1
			}
			return applySortOrder(order, sorting.SortOrder)
		}
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		if order := strings.Compare(b.CreatedAt, a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	}

	order := strings.Compare(subscriptionSortKey(sorting.SortBy, a), subscriptionSortKey(sorting.SortBy, b))
	order = applySortOrder(order, sorting.SortOrder)
	if order != 0 {
		return order
	}
	return strings.Compare(a.ID, b.ID)
}

// HandleListSubscriptions returns a handler for GET /api/v1/subscriptions.
func HandleListSubscriptions(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, ok := parseBoolQueryOrWriteInvalid(w, r, "enabled")
		if !ok {
			return
		}
		subs, err := cp.ListSubscriptions(nil)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		subs = filterSubscriptionsByKeyword(subs, r.URL.Query().Get("keyword"))
		summary := summarizeSubscriptions(subs)
		subs = filterSubscriptionsByEnabled(subs, enabled)

		sorting, ok := parseSortingOrWriteInvalid(
			w,
			r,
			[]string{"status", "name", "created_at", "last_checked", "last_updated"},
			"status",
			"asc",
		)
		if !ok {
			return
		}
		slices.SortStableFunc(subs, func(a, b service.SubscriptionResponse) int {
			return compareSubscriptionsForList(a, b, sorting)
		})

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WriteJSON(w, http.StatusOK, subscriptionListPageResponse{
			Items:   PaginateSlice(subs, pg),
			Total:   len(subs),
			Limit:   pg.Limit,
			Offset:  pg.Offset,
			Summary: summary,
		})
	}
}

// HandleGetSubscription returns a handler for GET /api/v1/subscriptions/{id}.
func HandleGetSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "subscription_id")
		if !ok {
			return
		}
		s, err := cp.GetSubscription(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, s)
	}
}

// HandleCreateSubscription returns a handler for POST /api/v1/subscriptions.
func HandleCreateSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.CreateSubscriptionRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		s, err := cp.CreateSubscription(req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, s)
	}
}

// HandleUpdateSubscription returns a handler for PATCH /api/v1/subscriptions/{id}.
func HandleUpdateSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "subscription_id")
		if !ok {
			return
		}
		body, ok := readRawBodyOrWriteInvalid(w, r)
		if !ok {
			return
		}
		s, err := cp.UpdateSubscription(id, body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, s)
	}
}

// HandleDeleteSubscription returns a handler for DELETE /api/v1/subscriptions/{id}.
func HandleDeleteSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "subscription_id")
		if !ok {
			return
		}
		if err := cp.DeleteSubscription(id); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleRefreshSubscription returns a handler for POST /api/v1/subscriptions/{id}/actions/refresh.
func HandleRefreshSubscription(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "subscription_id")
		if !ok {
			return
		}
		if err := cp.RefreshSubscription(id); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleCleanupSubscriptionCircuitOpenNodes returns a handler for
// POST /api/v1/subscriptions/{id}/actions/cleanup-circuit-open-nodes.
func HandleCleanupSubscriptionCircuitOpenNodes(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "subscription_id")
		if !ok {
			return
		}
		cleanedCount, err := cp.CleanupSubscriptionCircuitOpenNodes(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]int{"cleaned_count": cleanedCount})
	}
}
