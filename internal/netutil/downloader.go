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
	Client    *http.Client
	Timeout   time.Duration
	UserAgent string
}

// NewDirectDownloader creates a downloader with the given timeout.
func NewDirectDownloader(timeout time.Duration) *DirectDownloader {
	return &DirectDownloader{
		Client:  &http.Client{},
		Timeout: timeout,
	}
}

// Download fetches the URL and returns the response body.
func (d *DirectDownloader) Download(ctx context.Context, url string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && d.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &NonRetryableError{Err: err}
	}
	if d.UserAgent != "" {
		req.Header.Set("User-Agent", d.UserAgent)
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
