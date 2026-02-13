package state

import (
	"log"
	"sync"
	"time"
)

// CacheFlushWorker periodically flushes dirty sets to cache.db.
// It triggers a flush when:
//   - DirtyCount() >= threshold, OR
//   - time.Since(lastFlush) >= interval (and dirty count > 0)
//
// On Stop(), a final flush is performed before returning.
type CacheFlushWorker struct {
	engine    *StateEngine
	readers   CacheReaders
	threshold int
	interval  time.Duration
	checkTick time.Duration // how often to check conditions

	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewCacheFlushWorker creates a flush worker.
// checkTick controls how often flush conditions are evaluated (e.g. 5s).
func NewCacheFlushWorker(engine *StateEngine, readers CacheReaders, threshold int, interval time.Duration, checkTick time.Duration) *CacheFlushWorker {
	return &CacheFlushWorker{
		engine:    engine,
		readers:   readers,
		threshold: threshold,
		interval:  interval,
		checkTick: checkTick,
		stopCh:    make(chan struct{}),
	}
}

// Start launches the background flush goroutine.
func (w *CacheFlushWorker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop signals the worker to stop and performs a final flush.
// Blocks until the goroutine exits.
func (w *CacheFlushWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	w.wg.Wait()
}

func (w *CacheFlushWorker) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.checkTick)
	defer ticker.Stop()

	lastFlush := time.Now()

	for {
		select {
		case <-w.stopCh:
			// Final flush before exit.
			w.doFlush()
			return
		case <-ticker.C:
			dirty := w.engine.DirtyCount()
			if dirty == 0 {
				continue // Skip empty flush.
			}

			if dirty >= w.threshold || time.Since(lastFlush) >= w.interval {
				w.doFlush()
				lastFlush = time.Now()
			}
		}
	}
}

func (w *CacheFlushWorker) doFlush() {
	if err := w.engine.FlushDirtySets(w.readers); err != nil {
		log.Printf("[state] flush error (entries re-merged): %v", err)
	}
}
