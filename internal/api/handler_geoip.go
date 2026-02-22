package api

import (
	"fmt"
	"net/http"

	"github.com/Resinat/Resin/internal/service"
)

// HandleGeoIPStatus returns a handler for GET /api/v1/geoip/status.
func HandleGeoIPStatus(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := cp.GetGeoIPStatus()
		WriteJSON(w, http.StatusOK, status)
	}
}

// HandleGeoIPLookup returns a handler for GET /api/v1/geoip/lookup.
func HandleGeoIPLookup(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.URL.Query().Get("ip")
		if ip == "" {
			writeInvalidArgument(w, "ip query parameter is required")
			return
		}
		region, err := cp.LookupIP(ip)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{
			"ip":     ip,
			"region": region,
		})
	}
}

// HandleGeoIPUpdate returns a handler for POST /api/v1/geoip/actions/update-now.
func HandleGeoIPUpdate(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.UpdateGeoIPNow(); err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleGeoIPLookupPost returns a handler for POST /api/v1/geoip/lookup (batch).
func HandleGeoIPLookupPost(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IPs []string `json:"ips"`
		}
		if err := DecodeBody(r, &body); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		type result struct {
			IP     string `json:"ip"`
			Region string `json:"region"`
		}
		results := make([]result, 0, len(body.IPs))
		for i, ip := range body.IPs {
			region, err := cp.LookupIP(ip)
			if err != nil {
				writeInvalidArgument(w, fmt.Sprintf("ips[%d]: invalid IP address", i))
				return
			}
			results = append(results, result{IP: ip, Region: region})
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"results": results,
		})
	}
}
