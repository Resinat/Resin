package proxy

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/node"
)

type reverseLatencyReporter struct {
	health HealthRecorder
	hash   node.Hash
	domain string

	requestStart time.Time
	tlsStartUnix atomic.Int64
	reported     atomic.Bool
}

func newReverseLatencyReporter(health HealthRecorder, hash node.Hash, domain string) *reverseLatencyReporter {
	return &reverseLatencyReporter{
		health:       health,
		hash:         hash,
		domain:       domain,
		requestStart: time.Now(),
	}
}

func (r *reverseLatencyReporter) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		TLSHandshakeStart: func() {
			r.tlsStartUnix.Store(time.Now().UnixNano())
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				return
			}
			startUnix := r.tlsStartUnix.Load()
			if startUnix <= 0 {
				return
			}
			r.reportSince(time.Unix(0, startUnix))
		},
		GotFirstResponseByte: func() {
			r.reportSince(r.requestStart)
		},
	}
}

func (r *reverseLatencyReporter) reportSince(start time.Time) {
	if start.IsZero() {
		return
	}
	if !r.reported.CompareAndSwap(false, true) {
		return
	}
	latency := time.Since(start)
	if latency <= 0 {
		latency = time.Nanosecond
	}
	go r.health.RecordLatency(r.hash, r.domain, &latency)
}
