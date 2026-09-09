package netutil

import (
	"context"
	"errors"
	"time"

	"github.com/Resinat/Resin/internal/node"
)

// RetryDownloader decorates a Downloader with proxy retry logic.
type RetryDownloader struct {
	Direct Downloader
	// ProxyAttemptTimeout caps each proxy retry attempt duration.
	// If <= 0, it falls back to DirectDownloader's dynamic timeout when available,
	// otherwise 30s.
	ProxyAttemptTimeout time.Duration
	NodePicker          func(target string) (node.Hash, error)
	ProxyFetch          func(ctx context.Context, hash node.Hash, url string) ([]byte, error)
	ProxyFetchMetadata  func(ctx context.Context, hash node.Hash, url string) (DownloadResponse, error)
}

// Download attempts direct download first, then falls back to proxy retries.
func (r *RetryDownloader) Download(ctx context.Context, url string) ([]byte, error) {
	resp, err := r.DownloadWithMetadata(ctx, url)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DownloadWithMetadata attempts direct download first, then falls back to proxy
// retries while preserving response headers when the selected path provides them.
func (r *RetryDownloader) DownloadWithMetadata(ctx context.Context, url string) (DownloadResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	resp, err := r.directDownloadWithMetadata(ctx, url)
	if err == nil {
		return resp, nil
	}

	if !shouldRetryViaProxy(err) {
		return DownloadResponse{}, err
	}

	if r.NodePicker == nil || (r.ProxyFetch == nil && r.ProxyFetchMetadata == nil) {
		return DownloadResponse{}, err
	}

	// Respect caller cancellation/deadline: don't extend lifecycle beyond caller ctx.
	if ctx.Err() != nil {
		return DownloadResponse{}, err
	}

	attemptTimeout := r.proxyAttemptTimeout()

	// Retry 2 times with random proxy nodes.
	for i := 0; i < 2; i++ {
		if ctx.Err() != nil {
			return DownloadResponse{}, err
		}

		hash, pickErr := r.NodePicker(url)
		if pickErr != nil {
			continue
		}

		attemptCtx := ctx
		cancel := func() {}
		if attemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		}
		resp, fetchErr := r.proxyFetchWithMetadata(attemptCtx, hash, url)
		cancel()
		if fetchErr == nil {
			return resp, nil
		}
	}

	return DownloadResponse{}, err
}

func (r *RetryDownloader) directDownloadWithMetadata(ctx context.Context, url string) (DownloadResponse, error) {
	if direct, ok := r.Direct.(MetadataDownloader); ok {
		return direct.DownloadWithMetadata(ctx, url)
	}
	body, err := r.Direct.Download(ctx, url)
	if err != nil {
		return DownloadResponse{}, err
	}
	return DownloadResponse{Body: body}, nil
}

func (r *RetryDownloader) proxyFetchWithMetadata(ctx context.Context, hash node.Hash, url string) (DownloadResponse, error) {
	if r.ProxyFetchMetadata != nil {
		return r.ProxyFetchMetadata(ctx, hash, url)
	}
	body, err := r.ProxyFetch(ctx, hash, url)
	if err != nil {
		return DownloadResponse{}, err
	}
	return DownloadResponse{Body: body}, nil
}

func shouldRetryViaProxy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return shouldRetryHTTPStatusCode(statusErr.StatusCode)
	}

	var nonRetryable *NonRetryableError
	return !errors.As(err, &nonRetryable)
}

func shouldRetryHTTPStatusCode(statusCode int) bool {
	switch statusCode {
	case 403, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func (r *RetryDownloader) proxyAttemptTimeout() time.Duration {
	if r.ProxyAttemptTimeout > 0 {
		return r.ProxyAttemptTimeout
	}
	if direct, ok := r.Direct.(*DirectDownloader); ok && direct != nil {
		timeout := direct.currentTimeout()
		if timeout > 0 {
			return timeout
		}
	}
	return 30 * time.Second
}
