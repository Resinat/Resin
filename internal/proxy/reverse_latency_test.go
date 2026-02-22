package proxy

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
)

type latencySampleRecorder struct {
	samples chan time.Duration
}

func (r *latencySampleRecorder) RecordResult(hash node.Hash, success bool) {}

func (r *latencySampleRecorder) RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration) {
	if latency == nil {
		return
	}
	if *latency <= 0 {
		return
	}
	select {
	case r.samples <- *latency:
	default:
	}
}

func expectSample(t *testing.T, ch <-chan time.Duration) {
	t.Helper()
	select {
	case got := <-ch:
		if got <= 0 {
			t.Fatalf("latency sample <= 0: %v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected latency sample")
	}
}

func expectNoExtraSample(t *testing.T, ch <-chan time.Duration) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatalf("unexpected extra latency sample: %v", got)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestReverseLatencyReporter_FallbackOnFirstByte(t *testing.T) {
	rec := &latencySampleRecorder{samples: make(chan time.Duration, 4)}
	reporter := newReverseLatencyReporter(rec, node.Hash{1}, "example.com")
	trace := reporter.clientTrace()

	trace.GotFirstResponseByte()

	expectSample(t, rec.samples)
	expectNoExtraSample(t, rec.samples)
}

func TestReverseLatencyReporter_TLSPreferredAndSingleReport(t *testing.T) {
	rec := &latencySampleRecorder{samples: make(chan time.Duration, 4)}
	reporter := newReverseLatencyReporter(rec, node.Hash{2}, "example.com")
	trace := reporter.clientTrace()

	trace.TLSHandshakeStart()
	time.Sleep(time.Millisecond)
	trace.TLSHandshakeDone(tls.ConnectionState{}, nil)
	trace.GotFirstResponseByte()

	expectSample(t, rec.samples)
	expectNoExtraSample(t, rec.samples)
}

func TestReverseLatencyReporter_TLSFailureFallsBack(t *testing.T) {
	rec := &latencySampleRecorder{samples: make(chan time.Duration, 4)}
	reporter := newReverseLatencyReporter(rec, node.Hash{3}, "example.com")
	trace := reporter.clientTrace()

	trace.TLSHandshakeStart()
	trace.TLSHandshakeDone(tls.ConnectionState{}, errors.New("handshake failed"))
	trace.GotFirstResponseByte()

	expectSample(t, rec.samples)
	expectNoExtraSample(t, rec.samples)
}
