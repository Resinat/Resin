package api

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/resin-proxy/resin/internal/service"
)

func validateAccountPath(r *http.Request) (string, error) {
	account := PathParam(r, "account")
	if strings.TrimSpace(account) == "" {
		return "", invalidArgumentError("account: must be non-empty")
	}
	return account, nil
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

		// Optional exact-account filter.
		if raw := r.URL.Query().Get("account"); raw != "" {
			account := strings.TrimSpace(raw)
			if account == "" {
				writeInvalidArgument(w, "account query: must be non-empty when provided")
				return
			}
			filtered := make([]service.LeaseResponse, 0, len(leases))
			for _, l := range leases {
				if l.Account == account {
					filtered = append(filtered, l)
				}
			}
			leases = filtered
		}

		sorting, ok := parseSortingOrWriteInvalid(w, r, []string{"account", "expiry", "last_accessed"}, "expiry", "asc")
		if !ok {
			return
		}
		SortSlice(leases, sorting, func(l service.LeaseResponse) string {
			switch sorting.SortBy {
			case "expiry":
				return l.Expiry
			case "last_accessed":
				return l.LastAccessed
			default:
				return l.Account
			}
		})

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
		slices.SortStableFunc(entries, func(a, b service.IPLoadEntry) int {
			var order int
			switch sorting.SortBy {
			case "egress_ip":
				order = strings.Compare(a.EgressIP, b.EgressIP)
			default: // lease_count
				order = cmp.Compare(a.LeaseCount, b.LeaseCount)
			}
			if order == 0 {
				order = strings.Compare(a.EgressIP, b.EgressIP)
			}
			if sorting.SortOrder == "desc" {
				order = -order
			}
			return order
		})

		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, entries, pg)
	}
}
