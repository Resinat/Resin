package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestSummarizeUpstreamError_Canceled(t *testing.T) {
	detail := summarizeUpstreamError(context.Canceled)
	if detail.Kind != "canceled" {
		t.Fatalf("kind: got %q, want %q", detail.Kind, "canceled")
	}
	if detail.Message == "" {
		t.Fatal("expected non-empty message")
	}
}

func TestSummarizeUpstreamError_DNS(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host"}}
	detail := summarizeUpstreamError(err)
	if detail.Kind != "dns_error" {
		t.Fatalf("kind: got %q, want %q", detail.Kind, "dns_error")
	}
}

func TestSummarizeUpstreamError_Errno(t *testing.T) {
	err := &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}
	detail := summarizeUpstreamError(err)
	if detail.Kind != "connection_refused" {
		t.Fatalf("kind: got %q, want %q", detail.Kind, "connection_refused")
	}
	if detail.Errno != "ECONNREFUSED" {
		t.Fatalf("errno: got %q, want %q", detail.Errno, "ECONNREFUSED")
	}
}

func TestSanitizeUpstreamErrMsg_TruncatesAndNormalizes(t *testing.T) {
	raw := strings.Repeat("x", maxUpstreamErrMsgLen+20) + "\n\n"
	got := sanitizeUpstreamErrMsg(raw)
	if len(got) != maxUpstreamErrMsgLen {
		t.Fatalf("len: got %d, want %d", len(got), maxUpstreamErrMsgLen)
	}
	if strings.Contains(got, "\n") {
		t.Fatal("expected normalized single-line message")
	}
}

func TestIsBenignTunnelCopyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "net-closed", err: net.ErrClosed, want: true},
		{name: "context-canceled", err: context.Canceled, want: true},
		{name: "closed-network-connection", err: errors.New("write tcp: use of closed network connection"), want: true},
		{name: "generic", err: errors.New("connection reset by peer"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isBenignTunnelCopyError(tc.err)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
