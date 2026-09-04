package api

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/Resinat/Resin/internal/service"
)

func validateAccountPath(r *http.Request) (string, error) {
	account := PathParam(r, "account")
	if strings.TrimSpace(account) == "" {
		return "", invalidArgumentError("account: must be non-empty")
	}
	return account, nil
}

func leaseSortKey(sortBy string, l service.LeaseResponse) string {
	switch sortBy {
	case "node_tag":
		return l.NodeTag
	case "egress_ip":
		return l.EgressIP
	case "created_at":
		return l.CreatedAt
	case "expiry":
		return l.Expiry
	case "last_accessed":
		return l.LastAccessed
	default:
		return l.Account
	}
}

func sortLeaseResponsesByLatency(leases []service.LeaseResponse, sorting Sorting) {
	slices.SortStableFunc(leases, func(a, b service.LeaseResponse) int {
		if a.ReferenceLatencyMs == nil && b.ReferenceLatencyMs == nil {
			return strings.Compare(a.Account, b.Account)
		}
		if a.ReferenceLatencyMs == nil {
			return 1
		}
		if b.ReferenceLatencyMs == nil {
			return -1
		}
		order := cmp.Compare(*a.ReferenceLatencyMs, *b.ReferenceLatencyMs)
		if order != 0 {
			return applySortOrder(order, sorting.SortOrder)
		}
		return strings.Compare(a.Account, b.Account)
	})
}

func compareIPLoadEntries(sortBy string, a, b service.IPLoadEntry) int {
	switch sortBy {
	case "egress_ip":
		return strings.Compare(a.EgressIP, b.EgressIP)
	default: // lease_count
		order := cmp.Compare(a.LeaseCount, b.LeaseCount)
		if order != 0 {
			return order
		}
		return strings.Compare(a.EgressIP, b.EgressIP)
	}
}

func sortIPLoadEntries(entries []service.IPLoadEntry, sorting Sorting) {
	slices.SortStableFunc(entries, func(a, b service.IPLoadEntry) int {
		return applySortOrder(compareIPLoadEntries(sorting.SortBy, a, b), sorting.SortOrder)
	})
}

// HandleListLeases returns a handler for GET /api/v1/platforms/{id}/leases.
func HandleListLeases(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}

		leases, err := cp.ListLeases(platformID)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		fuzzy, ok := parseStrictBoolQuery(w, r, "fuzzy")
		if !ok {
			return
		}
		useFuzzyAccountMatch := fuzzy != nil && *fuzzy

		// Optional account filter.
		if raw := r.URL.Query().Get("account"); raw != "" {
			account := strings.TrimSpace(raw)
			if account == "" {
				writeInvalidArgument(w, "account query: must be non-empty when provided")
				return
			}
			filtered := make([]service.LeaseResponse, 0, len(leases))
			if useFuzzyAccountMatch {
				accountLower := strings.ToLower(account)
				for _, l := range leases {
					if strings.Contains(strings.ToLower(l.Account), accountLower) {
						filtered = append(filtered, l)
					}
				}
			} else {
				for _, l := range leases {
					if l.Account == account {
						filtered = append(filtered, l)
					}
				}
			}
			leases = filtered
		}

		sorting, ok := parseSortingOrWriteInvalid(w, r, []string{"account", "node_tag", "egress_ip", "reference_latency_ms", "created_at", "expiry", "last_accessed"}, "expiry", "asc")
		if !ok {
			return
		}
		if sorting.SortBy == "reference_latency_ms" {
			sortLeaseResponsesByLatency(leases, sorting)
		} else {
			SortSlice(leases, sorting, func(l service.LeaseResponse) string {
				return leaseSortKey(sorting.SortBy, l)
			})
		}

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, leases, pg)
	}
}

// HandleGetLease returns a handler for GET /api/v1/platforms/{id}/leases/{account}.
func HandleGetLease(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		account, err := validateAccountPath(r)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		lease, err := cp.GetLease(platformID, account)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, lease)
	}
}

// HandleDeleteLease returns a handler for DELETE /api/v1/platforms/{id}/leases/{account}.
func HandleDeleteLease(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		account, err := validateAccountPath(r)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if err := cp.DeleteLease(platformID, account); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleDeleteAllLeases returns a handler for DELETE /api/v1/platforms/{id}/leases.
func HandleDeleteAllLeases(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		if err := cp.DeleteAllLeases(platformID); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleBindLease returns a handler for PUT /api/v1/platforms/{id}/leases/{account}.
func HandleBindLease(cp *service.ControlPlaneService) http.HandlerFunc {
	type bindRequest struct {
		NodeHash string `json:"node_hash"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}
		account, err := validateAccountPath(r)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		var req bindRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		lease, err := cp.BindLease(platformID, account, req.NodeHash)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, lease)
	}
}

// HandleIPLoad returns a handler for GET /api/v1/platforms/{id}/ip-load.
func HandleIPLoad(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		platformID, ok := requireUUIDPathParam(w, r, "id", "platform_id")
		if !ok {
			return
		}

		entries, err := cp.GetIPLoad(platformID)
		if err != nil {
			writeServiceError(w, err)
			return
		}

		sorting, ok := parseSortingOrWriteInvalid(w, r, []string{"egress_ip", "lease_count"}, "lease_count", "desc")
		if !ok {
			return
		}
		sortIPLoadEntries(entries, sorting)

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, entries, pg)
	}
}
