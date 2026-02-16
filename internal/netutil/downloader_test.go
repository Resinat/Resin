package netutil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDirectDownloader_ContextDeadlineOverridesFallbackTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := NewDirectDownloader(
		func() time.Duration { return 20 * time.Millisecond },
		func() string { return "" },
	)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	body, err := d.Download(ctx, srv.URL)
	if err != nil {
		t.Fatalf("download should succeed with caller deadline, got err=%v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body: got %q, want %q", string(body), "ok")
	}
}

func TestDirectDownloader_FallbackTimeoutWithoutContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := NewDirectDownloader(
		func() time.Duration { return 20 * time.Millisecond },
		func() string { return "" },
	)

	_, err := d.Download(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestDirectDownloader_DynamicTimeoutPulled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	timeout := 200 * time.Millisecond
	d := NewDirectDownloader(
		func() time.Duration { return timeout },
		func() string { return "" },
	)

	if _, err := d.Download(context.Background(), srv.URL); err != nil {
		t.Fatalf("download should succeed with long timeout, got %v", err)
	}

	timeout = 20 * time.Millisecond
	_, err := d.Download(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected timeout error after shrinking dynamic timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestDirectDownloader_DynamicUserAgentPulled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	ua := "agent-a"
	d := NewDirectDownloader(
		func() time.Duration { return 0 },
		func() string { return ua },
	)

	body, err := d.Download(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("first download failed: %v", err)
	}
	if string(body) != "agent-a" {
		t.Fatalf("expected first UA agent-a, got %q", string(body))
	}

	ua = "agent-b"
	body, err = d.Download(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("second download failed: %v", err)
	}
	if string(body) != "agent-b" {
		t.Fatalf("expected second UA agent-b, got %q", string(body))
	}
}
