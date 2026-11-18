package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	sbOutbound "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// serverDialError is evidence from below the proxy protocol, not an inference
// from the error returned by a complete proxy handshake.
type serverDialError struct{ error }

func (e *serverDialError) Unwrap() error { return e.error }
func (e *serverDialError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.error, &timeout) && timeout.Timeout()
}

type serverDialer struct {
	sbOutbound.Adapter
	N.Dialer
}

type serverDNSRouter struct{ adapter.DNSRouter }

func (d *serverDNSRouter) Lookup(ctx context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	addresses, err := d.DNSRouter.Lookup(ctx, domain, options)
	var dns *net.DNSError
	if err == nil || errors.As(err, &dns) {
		return addresses, err
	}
	// A DNS transport may return its own TCP dial error without a DNSError.
	// Mark the resolver boundary before considering server-connect evidence.
	var timeout interface{ Timeout() bool }
	return nil, &net.DNSError{
		Name: domain, Err: err.Error(), UnwrapErr: err,
		IsTimeout: errors.As(err, &timeout) && timeout.Timeout(),
	}
}

func (d *serverDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := d.Dialer.DialContext(ctx, network, destination)
	if N.NetworkName(network) != N.NetworkTCP {
		return conn, err
	}
	if err != nil {
		return nil, markServerDialError(err)
	}
	if early, ok := common.Cast[N.EarlyConn](conn); ok && early.NeedHandshake() {
		// TFO returns a connection before the TCP connect runs. Its eventual
		// dial error has the same structured evidence as an eager dial.
		conn = &serverDialConn{Conn: conn}
	}
	return conn, nil
}

func markServerDialError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var dns *net.DNSError
	var op *net.OpError
	if !errors.As(err, &dns) && errors.As(err, &op) && op.Op == "dial" && N.NetworkName(op.Net) == N.NetworkTCP && op.Addr != nil {
		return &serverDialError{err}
	}
	return err
}

type serverDialConn struct{ net.Conn }

func (c *serverDialConn) Upstream() any { return c.Conn }
func (c *serverDialConn) NeedHandshake() bool {
	early, ok := common.Cast[N.EarlyConn](c.Conn)
	return ok && early.NeedHandshake()
}
func (c *serverDialConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, markServerDialError(err)
}
func (c *serverDialConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, markServerDialError(err)
}

func relayError(hash node.Hash, version uint64, err error) error {
	var known *node.RelayError
	if err == nil || hash.IsZero() || version == 0 || errors.Is(err, context.Canceled) || errors.As(err, &known) {
		return err
	}
	var serverDial *serverDialError
	return &node.RelayError{Hash: hash, Version: version, ServerDial: errors.As(err, &serverDial), Err: err}
}

func outboundVersion(ob adapter.Outbound) uint64 {
	if versioned, ok := ob.(interface{ RoutingVersion() uint64 }); ok {
		return versioned.RoutingVersion()
	}
	return 0
}
