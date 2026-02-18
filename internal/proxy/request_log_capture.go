package proxy

import (
	"bytes"
	"io"
	"net/http"
)

// captureRequestHeaders serializes headers to canonical wire format for
// request-log payload capture.
func captureRequestHeaders(header http.Header) []byte {
	if header == nil {
		return nil
	}
	var buf bytes.Buffer
	_ = header.Clone().Write(&buf)
	return buf.Bytes()
}

func captureHeadersWithLimit(header http.Header, maxBytes int) ([]byte, int, bool) {
	payload := captureRequestHeaders(header)
	totalLen := len(payload)
	if totalLen == 0 {
		return nil, 0, false
	}
	if maxBytes >= 0 && totalLen > maxBytes {
		return payload[:maxBytes], totalLen, true
	}
	return payload, totalLen, false
}

type payloadCaptureReadCloser struct {
	rc       io.ReadCloser
	maxBytes int
	payload  bytes.Buffer
	totalLen int
}

func newPayloadCaptureReadCloser(rc io.ReadCloser, maxBytes int) *payloadCaptureReadCloser {
	return &payloadCaptureReadCloser{
		rc:       rc,
		maxBytes: maxBytes,
	}
}

func (c *payloadCaptureReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.totalLen += n
		if c.maxBytes < 0 {
			_, _ = c.payload.Write(p[:n])
		} else {
			remaining := c.maxBytes - c.payload.Len()
			if remaining > 0 {
				if n <= remaining {
					_, _ = c.payload.Write(p[:n])
				} else {
					_, _ = c.payload.Write(p[:remaining])
				}
			}
		}
	}
	return n, err
}

func (c *payloadCaptureReadCloser) Close() error {
	return c.rc.Close()
}

func (c *payloadCaptureReadCloser) Payload() []byte {
	return c.payload.Bytes()
}

func (c *payloadCaptureReadCloser) TotalLen() int {
	return c.totalLen
}

func (c *payloadCaptureReadCloser) Truncated() bool {
	return c.totalLen > c.payload.Len()
}
