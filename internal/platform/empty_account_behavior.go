package platform

// ReverseProxyEmptyAccountBehavior controls how reverse proxy resolves account
// when the incoming reverse path omits Account.
type ReverseProxyEmptyAccountBehavior string

const (
	// ReverseProxyEmptyAccountBehaviorRandom keeps account empty and routes randomly.
	ReverseProxyEmptyAccountBehaviorRandom ReverseProxyEmptyAccountBehavior = "RANDOM"
	// ReverseProxyEmptyAccountBehaviorFixedHeader extracts account from one fixed request header.
	ReverseProxyEmptyAccountBehaviorFixedHeader ReverseProxyEmptyAccountBehavior = "FIXED_HEADER"
	// ReverseProxyEmptyAccountBehaviorAccountHeaderRule extracts account via account header rules.
	ReverseProxyEmptyAccountBehaviorAccountHeaderRule ReverseProxyEmptyAccountBehavior = "ACCOUNT_HEADER_RULE"
)

func (b ReverseProxyEmptyAccountBehavior) IsValid() bool {
	switch b {
	case ReverseProxyEmptyAccountBehaviorRandom,
		ReverseProxyEmptyAccountBehaviorFixedHeader,
		ReverseProxyEmptyAccountBehaviorAccountHeaderRule:
		return true
	default:
		return false
	}
}
