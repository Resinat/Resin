package testutil

import (
	"context"
	"errors"
	"net"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// NoopOutbound is a minimal adapter.Outbound implementation for tests that
// only need a non-nil outbound value.
type NoopOutbound struct{}

func (o *NoopOutbound) Type() string {
	return "noop"
}

func (o *NoopOutbound) Tag() string {
	return "noop"
}

func (o *NoopOutbound) Network() []string {
	return []string{"tcp", "udp"}
}

func (o *NoopOutbound) Dependencies() []string {
	return nil
}

func (o *NoopOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("noop outbound: dial not supported")
}

func (o *NoopOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("noop outbound: listen packet not supported")
}

func (o *NoopOutbound) Close() error {
	return nil
}

func NewNoopOutbound() adapter.Outbound {
	return &NoopOutbound{}
}
