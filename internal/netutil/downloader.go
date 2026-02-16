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
		return nil, &NonRetryableError{Err: err}
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
		return nil, fmt.Errorf("downloader: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, URL: url}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloader: %w", err)
	}
	return body, nil
}

func (d *DirectDownloader) currentTimeout() time.Duration {
	return d.TimeoutFn()
}

func (d *DirectDownloader) currentUserAgent() string {
	return d.UserAgentFn()
}
