package proxy

import (
	"net"
	"testing"
)

func TestFreeAccountFromPort(t *testing.T) {
	if got := freeAccountFromPort(21000); got != "__free_21000" {
		t.Errorf("freeAccountFromPort(21000) = %q, want __free_21000", got)
	}
}

func TestFreeAccountFromAddr(t *testing.T) {
	if got := freeAccountFromAddr(&net.TCPAddr{Port: 21005}); got != "__free_21005" {
		t.Errorf("freeAccountFromAddr(:21005) = %q, want __free_21005", got)
	}
	if got := freeAccountFromAddr(nil); got != freeAccountPrefix {
		t.Errorf("freeAccountFromAddr(nil) = %q, want %q", got, freeAccountPrefix)
	}
}
