package netutil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPStatusError indicates the server responded, but with an unexpected
// HTTP status code. This is a non-network failure.
type HTTPStatusError struct {
	StatusCode int
	URL        string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("downloader: unexpected status %d from %s", e.StatusCode, e.URL)
}

// NonRetryableError indicates direct request setup failed before any transport
// attempt was made (for example, malformed URL).
type NonRetryableError struct {
	Err error
}

func (e *NonRetryableError) Error() string {
	return fmt.Sprintf("downloader: %v", e.Err)
}

func (e *NonRetryableError) Unwrap() error {
	return e.Err
}

// Downloader fetches remote resources. Interface allows for proxy-aware
// implementations in later phases.
type Downloader interface {
	Download(ctx context.Context, url string) ([]byte, error)
}

// DownloadResponse contains the response body plus HTTP metadata relevant to
// callers that need subscription headers.
type DownloadResponse struct {
	Body   []byte
	Header http.Header
}

// MetadataDownloader fetches remote resources while preserving response
// metadata. Downloader remains the compatibility interface for body-only
// callers.
type MetadataDownloader interface {
	DownloadWithMetadata(ctx context.Context, url string) (DownloadResponse, error)
}

// DirectDownloader downloads via a standard HTTP client (no proxy).
type DirectDownloader struct {
	Client      *http.Client
	TimeoutFn   func() time.Duration
	UserAgentFn func() string
}

// NewDirectDownloader creates a downloader that pulls timeout/user-agent
// from callbacks on each request.
func NewDirectDownloader(timeoutFn func() time.Duration, userAgentFn func() string) *DirectDownloader {
	if timeoutFn == nil {
		panic("netutil: NewDirectDownloader requires non-nil timeoutFn")
	}
	if userAgentFn == nil {
		panic("netutil: NewDirectDownloader requires non-nil userAgentFn")
	}
	return &DirectDownloader{
		Client:      &http.Client{},
		TimeoutFn:   timeoutFn,
		UserAgentFn: userAgentFn,
	}
}

// Download fetches the URL and returns the response body.
func (d *DirectDownloader) Download(ctx context.Context, url string) ([]byte, error) {
	resp, err := d.DownloadWithMetadata(ctx, url)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DownloadWithMetadata fetches the URL and returns the response body and headers.
func (d *DirectDownloader) DownloadWithMetadata(ctx context.Context, url string) (DownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := d.currentTimeout()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DownloadResponse{}, &NonRetryableError{Err: err}
	}
	userAgent := d.currentUserAgent()
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}

	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return DownloadResponse{}, fmt.Errorf("downloader: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DownloadResponse{}, &HTTPStatusError{StatusCode: resp.StatusCode, URL: url}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DownloadResponse{}, fmt.Errorf("downloader: %w", err)
	}
	return DownloadResponse{
		Body:   body,
		Header: resp.Header.Clone(),
	}, nil
}

func (d *DirectDownloader) currentTimeout() time.Duration {
	return d.TimeoutFn()
}

func (d *DirectDownloader) currentUserAgent() string {
	return d.UserAgentFn()
}
