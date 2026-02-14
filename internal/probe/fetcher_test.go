package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDirectFetcher_HTTPFallbackLatencyNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	fetcher := DirectFetcher(func() time.Duration { return time.Second })
	body, latency, err := fetcher(nil, srv.URL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body: got %q, want %q", string(body), "ok")
	}
	if latency <= 0 {
		t.Fatalf("latency should be > 0, got %v", latency)
	}
}
