package service

import (
	"net/netip"
	"time"
)

// ------------------------------------------------------------------
// GeoIP
// ------------------------------------------------------------------

// GeoIPStatus is the API response for GeoIP status.
type GeoIPStatus struct {
	DBMtime             string `json:"db_mtime"`
	NextScheduledUpdate string `json:"next_scheduled_update"`
}

// GetGeoIPStatus returns the current GeoIP status.
func (s *ControlPlaneService) GetGeoIPStatus() GeoIPStatus {
	status := GeoIPStatus{}
	if t := s.GeoIP.LastUpdated(); !t.IsZero() {
		status.DBMtime = t.UTC().Format(time.RFC3339Nano)
	}
	if t := s.GeoIP.NextScheduledUpdate(); !t.IsZero() {
		status.NextScheduledUpdate = t.UTC().Format(time.RFC3339Nano)
	}
	return status
}

// LookupIP performs a GeoIP lookup.
func (s *ControlPlaneService) LookupIP(ipStr string) (string, error) {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return "", invalidArg("ip: invalid IP address")
	}
	region := s.GeoIP.Lookup(ip)
	return region, nil
}

// UpdateGeoIPNow triggers an immediate GeoIP database update (blocks).
func (s *ControlPlaneService) UpdateGeoIPNow() error {
	if err := s.GeoIP.UpdateNow(); err != nil {
		return internal("geoip update failed", err)
	}
	return nil
}
