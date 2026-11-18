package netutil

import (
	"context"
	"net/http/httptrace"
)

// WithoutHTTPTrace prevents protocol-internal HTTP traffic from contributing
// connection events to the outer request. Other context values are preserved.
func WithoutHTTPTrace(ctx context.Context) context.Context {
	// WithClientTrace also installs separate nettrace hooks. Replace those
	// through its public API without composing the outer trace into them.
	ctx = httptrace.WithClientTrace(withoutHTTPTrace{ctx}, &httptrace.ClientTrace{
		ConnectStart: func(string, string) {},
	})
	return withoutHTTPTrace{ctx}
}

type withoutHTTPTrace struct{ context.Context }

func (c withoutHTTPTrace) Value(key any) any {
	value := c.Context.Value(key)
	if _, ok := value.(*httptrace.ClientTrace); ok {
		return nil
	}
	return value
}
