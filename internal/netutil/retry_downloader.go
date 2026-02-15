package netutil

import (
	"context"
	"errors"
	"time"

	"github.com/resin-proxy/resin/internal/node"
)

// RetryDownloader decorates a Downloader with proxy retry logic.
type RetryDownloader struct {
	Direct Downloader
	// ProxyAttemptTimeout caps each proxy retry attempt duration.
	// If <= 0, it falls back to DirectDownloader.Timeout when available,
	// otherwise 30s.
	ProxyAttemptTimeout time.Duration
	NodePicker          func(target string) (node.Hash, error)
	ProxyFetch          func(ctx context.Context, hash node.Hash, url string) ([]byte, error)
}

// Download attempts direct download first, then falls back to proxy retries.
func (r *RetryDownloader) Download(ctx context.Context, url string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := r.Direct.Download(ctx, url)
	if err == nil {
		return body, nil
	}

	if !shouldRetryViaProxy(err) {
		return nil, err
	}

	if r.NodePicker == nil || r.ProxyFetch == nil {
		return nil, err
	}

	// Respect caller cancellation/deadline: don't extend lifecycle beyond caller ctx.
	if ctx.Err() != nil {
		return nil, err
	}

	attemptTimeout := r.proxyAttemptTimeout()

	// Retry 2 times with random proxy nodes.
	for i := 0; i < 2; i++ {
		if ctx.Err() != nil {
			return nil, err
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
		body, fetchErr := r.ProxyFetch(attemptCtx, hash, url)
		cancel()
		if fetchErr == nil {
			return body, nil
		}
	}

	return nil, err
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
		return false
	}

	var nonRetryable *NonRetryableError
	return !errors.As(err, &nonRetryable)
}

func (r *RetryDownloader) proxyAttemptTimeout() time.Duration {
	if r.ProxyAttemptTimeout > 0 {
		return r.ProxyAttemptTimeout
	}
	if direct, ok := r.Direct.(*DirectDownloader); ok && direct != nil && direct.Timeout > 0 {
		return direct.Timeout
	}
	return 30 * time.Second
}
