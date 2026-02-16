package proxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestShouldRecordForwardCopyFailure(t *testing.T) {
	t.Run("NilError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if shouldRecordForwardCopyFailure(req, nil) {
			t.Fatal("nil copy error should not be recorded as failure")
		}
	})

	t.Run("ClientCanceledContext", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest("GET", "http://example.com", nil).WithContext(ctx)
		if shouldRecordForwardCopyFailure(req, genericErr{}) {
			t.Fatal("client-canceled request should not penalize node health")
		}
	})

	t.Run("ContextCanceledError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if shouldRecordForwardCopyFailure(req, canceledErr{}) {
			t.Fatal("context canceled errors should not penalize node health")
		}
	})

	t.Run("UpstreamTimeout", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		if !shouldRecordForwardCopyFailure(req, deadlineExceededErr{}) {
			t.Fatal("upstream timeout should be recorded as node failure")
		}
	})

	t.Run("GenericUpstreamError", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		err := errors.New("upstream read failed")
		if !shouldRecordForwardCopyFailure(req, err) {
			t.Fatal("generic upstream read error should be recorded as node failure")
		}
	})
}
