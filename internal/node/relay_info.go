package node

import (
	"context"
	"errors"
	"net"

	"github.com/sagernet/sing/common"
)

// RelayInfo identifies the actual relay outbound used by a connection.
// A zero value means it could not be observed, not that the connection is direct.
type RelayInfo struct {
	Hash    Hash
	Tag     string
	Version uint64
}

// WrapError adds connection identity to errors synthesized above the socket.
// Explicit failure evidence takes priority over connection metadata.
func (info RelayInfo) WrapError(err error) error {
	var known *RelayError
	if err == nil || info.Hash.IsZero() || info.Version == 0 || errors.Is(err, context.Canceled) || errors.As(err, &known) {
		return err
	}
	return &RelayError{Hash: info.Hash, Version: info.Version, Err: err}
}

// RelayInfoFromConn follows supported connection wrappers, including TLS.
func RelayInfoFromConn(conn net.Conn) RelayInfo {
	if marked, ok := common.Cast[interface{ RelayInfo() RelayInfo }](conn); ok {
		return marked.RelayInfo()
	}
	return RelayInfo{}
}
