package requestlog

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/resin-proxy/resin/internal/proxy"
)

// Service provides an async request log writer that implements proxy.EventEmitter.
// EmitRequestLog performs a non-blocking channel send (drops on overflow).
// A background goroutine flushes batches to the Repo.
type Service struct {
	repo      *Repo
	queue     chan proxy.RequestLogEntry
	batchSize int
	interval  time.Duration

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// ServiceConfig configures the request log service.
type ServiceConfig struct {
	Repo          *Repo
	QueueSize     int
	FlushBatch    int
	FlushInterval time.Duration
}

// NewService creates a new request log service.
func NewService(cfg ServiceConfig) *Service {
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 8192
	}
	batchSize := cfg.FlushBatch
	if batchSize <= 0 {
		batchSize = 4096
	}
	interval := cfg.FlushInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Service{
		repo:      cfg.Repo,
		queue:     make(chan proxy.RequestLogEntry, queueSize),
		batchSize: batchSize,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
}

// Start launches the background flush goroutine.
func (s *Service) Start() {
	s.wg.Add(1)
	go s.flushLoop()
}

// Stop signals the flush loop to stop, drains remaining entries, and returns.
func (s *Service) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// EmitRequestLog enqueues a log entry. Non-blocking; drops on overflow.
func (s *Service) EmitRequestLog(entry proxy.RequestLogEntry) {
	select {
	case s.queue <- entry:
	default:
		// Queue full — drop entry to avoid blocking hot path.
	}
}

// EmitRequestFinished is a no-op; finished events are handled by MetricsManager.
func (s *Service) EmitRequestFinished(proxy.RequestFinishedEvent) {}

// flushLoop runs until stopCh is closed, flushing on batch-size or timer.
func (s *Service) flushLoop() {
	defer s.wg.Done()

	batch := make([]proxy.RequestLogEntry, 0, s.batchSize)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case entry := <-s.queue:
			batch = append(batch, entry)
			if len(batch) >= s.batchSize {
				s.flush(batch)
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				s.flush(batch)
				batch = batch[:0]
			}

		case <-s.stopCh:
			// Drain remaining.
			s.drainAndFlush(batch)
			return
		}
	}
}

func (s *Service) drainAndFlush(batch []proxy.RequestLogEntry) {
	for {
		select {
		case entry := <-s.queue:
			batch = append(batch, entry)
			if len(batch) >= s.batchSize {
				s.flush(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				s.flush(batch)
			}
			return
		}
	}
}

func (s *Service) flush(entries []proxy.RequestLogEntry) {
	rows := make([]LogRow, len(entries))
	for i, e := range entries {
		rows[i] = LogRow{
			ID:           uuid.New().String(),
			TsNs:         e.StartedAtNs,
			ProxyType:    int(e.ProxyType),
			ClientIP:     e.ClientIP,
			PlatformID:   e.PlatformID,
			PlatformName: e.PlatformName,
			Account:      e.Account,
			TargetHost:   e.TargetHost,
			TargetURL:    e.TargetURL,
			NodeHash:     e.NodeHash,
			NodeTag:      e.NodeTag,
			EgressIP:     e.EgressIP,
			DurationNs:   e.DurationNs,
			NetOK:        e.NetOK,
			HTTPMethod:   e.HTTPMethod,
			HTTPStatus:   e.HTTPStatus,

			ReqHeadersLen:        e.ReqHeadersLen,
			ReqBodyLen:           e.ReqBodyLen,
			RespHeadersLen:       e.RespHeadersLen,
			RespBodyLen:          e.RespBodyLen,
			ReqHeadersTruncated:  e.ReqHeadersTruncated,
			ReqBodyTruncated:     e.ReqBodyTruncated,
			RespHeadersTruncated: e.RespHeadersTruncated,
			RespBodyTruncated:    e.RespBodyTruncated,

			ReqHeaders:  e.ReqHeaders,
			ReqBody:     e.ReqBody,
			RespHeaders: e.RespHeaders,
			RespBody:    e.RespBody,
		}
	}

	if n, err := s.repo.InsertBatch(rows); err != nil {
		log.Printf("[requestlog] flush %d entries failed: %v", len(rows), err)
	} else if n > 0 {
		log.Printf("[requestlog] flushed %d entries", n)
	}
}

// Repo returns the underlying repository for query access.
func (s *Service) Repo() *Repo {
	return s.repo
}
