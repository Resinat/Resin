package proxy

import (
	"errors"
	"net"
)

var errHalfCloseUnsupported = errors.New("half-close unsupported")

func closeWriteErr(conn net.Conn) error {
	if conn == nil {
		return net.ErrClosed
	}
	closeWriter, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		return errHalfCloseUnsupported
	}
	return closeWriter.CloseWrite()
}

func closeReadErr(conn net.Conn) error {
	if conn == nil {
		return net.ErrClosed
	}
	closeReader, ok := conn.(interface{ CloseRead() error })
	if !ok {
		return errHalfCloseUnsupported
	}
	return closeReader.CloseRead()
}
