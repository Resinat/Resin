package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
)

const maxUpstreamErrMsgLen = 512

type upstreamErrorDetail struct {
	Kind    string
	Errno   string
	Message string
}

func summarizeUpstreamError(err error) upstreamErrorDetail {
	if err == nil {
		return upstreamErrorDetail{}
	}
	detail := upstreamErrorDetail{
		Errno:   extractErrnoCode(err),
		Message: sanitizeUpstreamErrMsg(err.Error()),
	}
	detail.Kind = classifyUpstreamErrKind(err, detail.Errno)
	return detail
}

func classifyUpstreamErrKind(err error, errno string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns_error"
	}

	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		return "tls_cert_invalid"
	}
	var unknownAuthErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthErr) {
		return "tls_unknown_authority"
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return "tls_hostname_invalid"
	}
	var recordHeaderErr tls.RecordHeaderError
	if errors.As(err, &recordHeaderErr) {
		return "tls_record_header"
	}

	switch errno {
	case "ECONNREFUSED":
		return "connection_refused"
	case "ECONNRESET":
		return "connection_reset"
	case "ECONNABORTED":
		return "connection_aborted"
	case "ENETUNREACH":
		return "network_unreachable"
	case "EHOSTUNREACH":
		return "host_unreachable"
	case "EPIPE":
		return "broken_pipe"
	case "ETIMEDOUT":
		return "timeout"
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		switch strings.ToLower(opErr.Op) {
		case "dial":
			return "dial_error"
		case "read":
			return "read_error"
		case "write":
			return "write_error"
		default:
			return "net_op_error"
		}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "url_error"
	}

	return "network_error"
}

func extractErrnoCode(err error) string {
	if err == nil {
		return ""
	}

	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	return normalizeErrno(errno)
}

func normalizeErrno(errno syscall.Errno) string {
	switch errno {
	case syscall.ECONNREFUSED:
		return "ECONNREFUSED"
	case syscall.ECONNRESET:
		return "ECONNRESET"
	case syscall.ECONNABORTED:
		return "ECONNABORTED"
	case syscall.ENETUNREACH:
		return "ENETUNREACH"
	case syscall.EHOSTUNREACH:
		return "EHOSTUNREACH"
	case syscall.EPIPE:
		return "EPIPE"
	case syscall.ETIMEDOUT:
		return "ETIMEDOUT"
	default:
		return fmt.Sprintf("ERRNO_%d", int(errno))
	}
}

func sanitizeUpstreamErrMsg(raw string) string {
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if raw == "" {
		return ""
	}
	if len(raw) > maxUpstreamErrMsgLen {
		return raw[:maxUpstreamErrMsgLen]
	}
	return raw
}

func isBenignTunnelCopyError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "closed network connection")
}
