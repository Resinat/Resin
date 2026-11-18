package outbound

import (
	"net"
	"os"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

// RelayInfo is the immutable identity of the relay used by one connection.
// A zero value means it could not be observed, not that the connection is direct.
type RelayInfo struct {
	Hash node.Hash
	Tag  string
}

type relayConn struct {
	net.Conn
	info RelayInfo
}

func (c *relayConn) RelayInfo() RelayInfo { return c.info }
func (c *relayConn) Upstream() any        { return c.Conn }
func (c *relayConn) CloseWrite() error {
	if writer, ok := common.Cast[N.WriteCloser](c.Conn); ok {
		return writer.CloseWrite()
	}
	return os.ErrInvalid
}
func (c *relayConn) CloseRead() error {
	if reader, ok := common.Cast[N.ReadCloser](c.Conn); ok {
		return reader.CloseRead()
	}
	return os.ErrInvalid
}

// RelayInfoFromConn follows supported connection wrappers, including TLS.
// Protocols that hide their underlying session deliberately return no identity.
func RelayInfoFromConn(conn net.Conn) RelayInfo {
	if marked, ok := common.Cast[interface{ RelayInfo() RelayInfo }](conn); ok {
		return marked.RelayInfo()
	}
	return RelayInfo{}
}
