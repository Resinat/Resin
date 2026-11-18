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
	// Binding is present only when observed for this dial or connection.
	Binding ChainBinding
}

// ChainBinding revokes one binding generation without affecting its successor.
type ChainBinding interface{ Invalidate() }

type bindingError struct {
	err     error
	binding ChainBinding
}

func (e *bindingError) Error() string         { return e.err.Error() }
func (e *bindingError) Unwrap() error         { return e.err }
func (e *bindingError) Binding() ChainBinding { return e.binding }
func (e *bindingError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.err, &timeout) && timeout.Timeout()
}

func BindingFromError(err error) ChainBinding {
	var marked interface{ Binding() ChainBinding }
	if errors.As(err, &marked) {
		return marked.Binding()
	}
	return nil
}

// WrapError adds connection identity to errors synthesized above the socket.
// Explicit failure evidence takes priority over connection metadata.
func (info RelayInfo) WrapError(err error) error {
	var known *RelayError
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	if !info.Hash.IsZero() && info.Version != 0 && !errors.As(err, &known) {
		err = &RelayError{Hash: info.Hash, Version: info.Version, Err: err}
	}
	if info.Binding != nil && BindingFromError(err) == nil {
		err = &bindingError{err: err, binding: info.Binding}
	}
	return err
}

// RelayInfoFromConn follows supported connection wrappers, including TLS.
func RelayInfoFromConn(conn net.Conn) RelayInfo {
	if marked, ok := common.Cast[interface{ RelayInfo() RelayInfo }](conn); ok {
		return marked.RelayInfo()
	}
	return RelayInfo{}
}
