package config

import "strings"

// AuthVersion represents proxy auth parsing behavior.
type AuthVersion string

const (
	AuthVersionV1 AuthVersion = "V1"
)

// NormalizeAuthVersion trims and normalizes auth version values.
// Returns empty when value is not recognized.
func NormalizeAuthVersion(raw string) AuthVersion {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(AuthVersionV1):
		return AuthVersionV1
	default:
		return ""
	}
}
