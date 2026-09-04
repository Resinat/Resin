package netutil

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
)

type downloaderFunc func(ctx context.Context, url string) ([]byte, error)

func (f downloaderFunc) Download(ctx context.Context, url string) ([]byte, error) {
	return f(ctx, url)
}

func TestRetryDownloader_RetryOnSelectedHTTPStatusError(t *testing.T) {
	retryableStatusCodes := []int{403, 429, 500, 502, 503, 504}

	for _, statusCode := range retryableStatusCodes {
		t.Run(strconv.Itoa(statusCode), func(t *testing.T) {
			var pickerCalls, proxyCalls int

			r := &RetryDownloader{
				Direct: downloaderFunc(func(_ context.Context, url string) ([]byte, error) {
					return nil, &HTTPStatusError{StatusCode: statusCode, URL: url}
				}),
				NodePicker: func(_ string) (node.Hash, error) {
					pickerCalls++
					return node.HashFromRawOptions([]byte(`{"id":"retry-node"}`)), nil
				},
				ProxyFetch: func(_ context.Context, _ node.Hash, _ string) ([]byte, error) {
					proxyCalls++
					return []byte("proxy"), nil
				},
			}

			body, err := r.Download(context.Background(), "https://example.com")
			if err != nil {
				t.Fatalf("expected proxy retry success, got %v", err)
			}
			if string(body) != "proxy" {
				t.Fatalf("unexpected body %q", string(body))
			}
			if pickerCalls != 1 || proxyCalls != 1 {
				t.Fatalf("expected single successful retry, got picker=%d proxy=%d", pickerCalls, proxyCalls)
			}
		})
	}
}

func TestRetryDownloader_NoRetryOnNonRetryableHTTPStatusError(t *testing.T) {
	var pickerCalls, proxyCalls int

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, url string) ([]byte, error) {
			return nil, &HTTPStatusError{StatusCode: 404, URL: url}
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.Zero, nil
		},
		ProxyFetch: func(_ context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			return []byte("proxy"), nil
		},
	}

	_, err := r.Download(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("expected direct error")
	}
	if pickerCalls != 0 || proxyCalls != 0 {
		t.Fatalf("expected no proxy retry, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}

func TestRetryDownloader_NoRetryOnNonRetryableError(t *testing.T) {
	var pickerCalls, proxyCalls int
	inner := errors.New("bad url")

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, &NonRetryableError{Err: inner}
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.Zero, nil
		},
		ProxyFetch: func(_ context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			return []byte("proxy"), nil
		},
	}

	_, err := r.Download(context.Background(), "::::")
	if err == nil {
		t.Fatal("expected direct error")
	}
	if !errors.Is(err, inner) {
		t.Fatalf("expected wrapped inner error, got: %v", err)
	}
	if pickerCalls != 0 || proxyCalls != 0 {
		t.Fatalf("expected no proxy retry, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}

func TestRetryDownloader_RetryOnNetworkError(t *testing.T) {
	var pickerCalls, proxyCalls int

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.HashFromRawOptions([]byte(`{"id":"retry-node"}`)), nil
		},
		ProxyFetch: func(_ context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			return []byte("via-proxy"), nil
		},
	}

	body, err := r.Download(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("expected proxy retry success, got %v", err)
	}
	if string(body) != "via-proxy" {
		t.Fatalf("unexpected body %q", string(body))
	}
	if pickerCalls != 1 || proxyCalls != 1 {
		t.Fatalf("expected single successful retry, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}

func TestRetryDownloader_DownloadWithMetadataPreservesProxyHeaders(t *testing.T) {
	var pickerCalls, proxyCalls int

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.HashFromRawOptions([]byte(`{"id":"retry-node-metadata"}`)), nil
		},
		ProxyFetchMetadata: func(_ context.Context, _ node.Hash, _ string) (DownloadResponse, error) {
			proxyCalls++
			header := http.Header{}
			header.Set("Subscription-Userinfo", "upload=1; download=2; total=3")
			return DownloadResponse{Body: []byte("via-proxy"), Header: header}, nil
		},
	}

	resp, err := r.DownloadWithMetadata(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("expected proxy metadata success, got %v", err)
	}
	if string(resp.Body) != "via-proxy" {
		t.Fatalf("unexpected body %q", string(resp.Body))
	}
	if got := resp.Header.Get("Subscription-Userinfo"); got != "upload=1; download=2; total=3" {
		t.Fatalf("unexpected subscription userinfo %q", got)
	}
	if pickerCalls != 1 || proxyCalls != 1 {
		t.Fatalf("expected single successful retry, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}

func TestRetryDownloader_NoRetryWhenContextDone(t *testing.T) {
	var pickerCalls int
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.Canceled
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.Zero, nil
		},
	}

	_, err := r.Download(ctx, "https://example.com")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if pickerCalls != 0 {
		t.Fatalf("expected no retry when context is done, got picker calls=%d", pickerCalls)
	}
}

func TestRetryDownloader_ProxyRetriesExhaustedReturnsDirectError(t *testing.T) {
	var pickerCalls, proxyCalls int
	directErr := context.DeadlineExceeded

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, directErr
		}),
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.HashFromRawOptions([]byte(`{"id":"retry-node"}`)), nil
		},
		ProxyFetch: func(_ context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			return nil, errors.New("proxy failed")
		},
	}

	_, err := r.Download(context.Background(), "https://example.com")
	if !errors.Is(err, directErr) {
		t.Fatalf("expected original direct error, got %v", err)
	}
	if pickerCalls != 2 {
		t.Fatalf("expected 2 picker attempts, got %d", pickerCalls)
	}
	if proxyCalls != 2 {
		t.Fatalf("expected 2 proxy fetch attempts, got %d", proxyCalls)
	}
}

func TestRetryDownloader_NoRetryWhenCallerDeadlineExceeded(t *testing.T) {
	var pickerCalls, proxyCalls int

	r := &RetryDownloader{
		Direct: downloaderFunc(func(ctx context.Context, _ string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		ProxyAttemptTimeout: 100 * time.Millisecond,
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.HashFromRawOptions([]byte(`{"id":"retry-node-deadline"}`)), nil
		},
		ProxyFetch: func(ctx context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return []byte("via-proxy"), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := r.Download(ctx, "https://example.com")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if pickerCalls != 0 || proxyCalls != 0 {
		t.Fatalf("expected no proxy retry after caller deadline, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}

func TestRetryDownloader_ProxyAttemptTimeoutStillApplies(t *testing.T) {
	var pickerCalls, proxyCalls int

	r := &RetryDownloader{
		Direct: downloaderFunc(func(_ context.Context, _ string) ([]byte, error) {
			return nil, context.DeadlineExceeded
		}),
		ProxyAttemptTimeout: 20 * time.Millisecond,
		NodePicker: func(_ string) (node.Hash, error) {
			pickerCalls++
			return node.HashFromRawOptions([]byte(`{"id":"retry-node-attempt-timeout"}`)), nil
		},
		ProxyFetch: func(ctx context.Context, _ node.Hash, _ string) ([]byte, error) {
			proxyCalls++
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("missing per-attempt deadline")
			}
			if proxyCalls == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return []byte("via-proxy"), nil
		},
	}

	body, err := r.Download(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("expected proxy retry success, got %v", err)
	}
	if string(body) != "via-proxy" {
		t.Fatalf("unexpected body %q", string(body))
	}
	if pickerCalls != 2 || proxyCalls != 2 {
		t.Fatalf("expected two timed attempts, got picker=%d proxy=%d", pickerCalls, proxyCalls)
	}
}
