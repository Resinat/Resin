package node

import (
	"errors"
	"fmt"
)

// RelayError identifies the actual relay outbound involved in a failed call.
// ServerDial is true only when its underlying server dialer returned the error;
// protocol handshakes and subsequent I/O do not establish that attribution.
type RelayError struct {
	Hash       Hash
	Version    uint64
	ServerDial bool
	Err        error
}

func (e *RelayError) Error() string { return fmt.Sprintf("chain relay %s: %v", e.Hash, e.Err) }
func (e *RelayError) Unwrap() error { return e.Err }
func (e *RelayError) Timeout() bool {
	var timeout interface{ Timeout() bool }
	return errors.As(e.Err, &timeout) && timeout.Timeout()
}
