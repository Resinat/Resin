package probe

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
)

// ParseCloudflareTrace parses a Cloudflare /cdn-cgi/trace response body
// and extracts the "ip" field plus optional "loc" field.
//
// Example response body:
//
//	fl=123
//	h=1.2.3.4
//	ip=203.0.113.1
//	ts=1234567890
//	...
func ParseCloudflareTrace(body []byte) (netip.Addr, *string, error) {
	var (
		ip       netip.Addr
		ipFound  bool
		locValue string
		locSet   bool
	)

	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("ip=")) {
			raw := string(bytes.TrimSpace(line[3:]))
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return netip.Addr{}, nil, err
			}
			ip = addr
			ipFound = true
			continue
		}
		if bytes.HasPrefix(line, []byte("loc=")) {
			locValue = strings.ToLower(strings.TrimSpace(string(line[4:])))
			locSet = true
		}
	}

	if !ipFound {
		return netip.Addr{}, nil, errors.New("probe: ip field not found in trace response")
	}
	if !locSet || locValue == "" {
		return ip, nil, nil
	}
	return ip, &locValue, nil
}

// ParsePlainIP parses probe endpoints that return only the caller IP.
func ParsePlainIP(body []byte) (netip.Addr, *string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return netip.Addr{}, nil, err
	}
	return addr, nil, nil
}
