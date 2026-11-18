package outbound

import (
	"errors"
	"io"
	"net"
	"os"

	"github.com/Resinat/Resin/internal/node"
	N "github.com/sagernet/sing/common/network"
)

type relayConn struct {
	net.Conn
	info node.RelayInfo
}

func (c *relayConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.wrapError(err)
}
func (c *relayConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.wrapError(err)
}
func (c *relayConn) wrapError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return err
	}
	return c.info.WrapError(relayError(c.info.Hash, c.info.Version, err))
}

func (c *relayConn) RelayInfo() node.RelayInfo { return c.info }
func (c *relayConn) Upstream() any             { return c.Conn }
func (c *relayConn) CloseWrite() error {
	if writer, ok := c.Conn.(N.WriteCloser); ok {
		return writer.CloseWrite()
	}
	return os.ErrInvalid
}
func (c *relayConn) CloseRead() error {
	if reader, ok := c.Conn.(N.ReadCloser); ok {
		return reader.CloseRead()
	}
	return os.ErrInvalid
}
