package testutil

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

// StubOutboundBuilder creates a real dial-capable outbound for tests.
type StubOutboundBuilder struct{}

type stubOutbound struct {
	dialer net.Dialer
}

func (s *stubOutbound) Type() string {
	return "stub"
}

func (s *stubOutbound) Tag() string {
	return "stub"
}

func (s *stubOutbound) Network() []string {
	return []string{"tcp", "udp"}
}

func (s *stubOutbound) Dependencies() []string {
	return nil
}

func (s *stubOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return s.dialer.DialContext(ctx, network, destination.String())
}

func (s *stubOutbound) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	var lc net.ListenConfig
	return lc.ListenPacket(ctx, "udp", "")
}

func (s *stubOutbound) Close() error {
	return nil
}

func (b *StubOutboundBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	return &stubOutbound{
		dialer: net.Dialer{Timeout: 30 * time.Second},
	}, nil
}
