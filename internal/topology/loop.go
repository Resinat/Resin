package topology

import (
	"math/rand/v2"
	"time"
)

// Scan loop timing constants.
const (
	// scanIntervalMinMs is the minimum jittered scan interval in milliseconds.
	scanIntervalMinMs = 13000
	// scanIntervalJitterMs is the random jitter range added to the minimum.
	scanIntervalJitterMs = 4000
	// schedulerLookahead is subtracted from a subscription's next-due time so the
	// scheduler can batch work slightly ahead of the true deadline.
	schedulerLookahead = 15 * time.Second
)

// runLoop calls fn repeatedly with a jittered interval between scanIntervalMinMs
// and (scanIntervalMinMs + scanIntervalJitterMs) milliseconds, until stopCh is closed.
func runLoop(stopCh <-chan struct{}, fn func()) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // drain initial fire

	for {
		jitter := time.Duration(scanIntervalMinMs+rand.IntN(scanIntervalJitterMs)) * time.Millisecond
		timer.Reset(jitter)
		select {
		case <-stopCh:
			return
		case <-timer.C:
		}
		fn()
	}
}
