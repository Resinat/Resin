package proxy

import "context"

// InboundPolicy carries endpoint-specific behavior into shared proxy handlers.
type InboundPolicy struct {
	RequireProxyAuthInfo bool
}

type inboundPolicyContextKey struct{}

func ContextWithInboundPolicy(ctx context.Context, policy InboundPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, inboundPolicyContextKey{}, policy)
}

func InboundPolicyFromContext(ctx context.Context) InboundPolicy {
	if ctx == nil {
		return InboundPolicy{}
	}
	policy, _ := ctx.Value(inboundPolicyContextKey{}).(InboundPolicy)
	return policy
}
