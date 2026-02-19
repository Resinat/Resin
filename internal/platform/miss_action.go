package platform

// ReverseProxyMissAction controls how reverse proxy handles requests whose
// account cannot be resolved from path/header match rules.
type ReverseProxyMissAction string

const (
	ReverseProxyMissActionRandom ReverseProxyMissAction = "RANDOM"
	ReverseProxyMissActionReject ReverseProxyMissAction = "REJECT"
)

func (a ReverseProxyMissAction) IsValid() bool {
	switch a {
	case ReverseProxyMissActionRandom, ReverseProxyMissActionReject:
		return true
	default:
		return false
	}
}
