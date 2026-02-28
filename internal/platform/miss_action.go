package platform

import "strings"

// ReverseProxyMissAction controls how reverse proxy handles requests whose
// account cannot be resolved from path/header match rules.
type ReverseProxyMissAction string

const (
	// ReverseProxyMissActionTreatAsEmpty keeps routing as empty-account flow when
	// account extraction fails.
	ReverseProxyMissActionTreatAsEmpty ReverseProxyMissAction = "TREAT_AS_EMPTY"
	ReverseProxyMissActionReject       ReverseProxyMissAction = "REJECT"
)

func (a ReverseProxyMissAction) IsValid() bool {
	switch a {
	case ReverseProxyMissActionTreatAsEmpty, ReverseProxyMissActionReject:
		return true
	default:
		return false
	}
}

func NormalizeReverseProxyMissAction(raw string) ReverseProxyMissAction {
	v := ReverseProxyMissAction(strings.TrimSpace(raw))
	if v.IsValid() {
		return v
	}
	return ""
}
