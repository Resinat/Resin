package probe

import (
	"bytes"
	"errors"
	"net/netip"
)

// ParseCloudflareTrace parses a Cloudflare /cdn-cgi/trace response body
// and extracts the "ip" field as a netip.Addr.
//
// Example response body:
//
//	fl=123
//	h=1.2.3.4
//	ip=203.0.113.1
//	ts=1234567890
//	...
func ParseCloudflareTrace(body []byte) (netip.Addr, error) {
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("ip=")) {
			raw := string(bytes.TrimSpace(line[3:]))
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return netip.Addr{}, err
			}
			return addr, nil
		}
	}
	return netip.Addr{}, errors.New("probe: ip field not found in trace response")
}
