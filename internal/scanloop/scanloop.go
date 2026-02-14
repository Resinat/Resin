package scanloop

import (
	"math/rand/v2"
	"time"
)

const (
	// DefaultMinInterval and DefaultJitterRange define the shared scan cadence.
	DefaultMinInterval = 13 * time.Second
	DefaultJitterRange = 4 * time.Second
)

// Run executes fn at a jittered interval until stopCh is closed.
// The interval is: minInterval + random([0, jitterRange)).
func Run(stopCh <-chan struct{}, minInterval, jitterRange time.Duration, fn func()) {
	if minInterval <= 0 {
		minInterval = time.Second
	}
	if jitterRange < 0 {
		jitterRange = 0
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // drain initial fire

	for {
		interval := minInterval
		if jitterRange > 0 {
			interval += time.Duration(rand.Int64N(int64(jitterRange)))
		}

		timer.Reset(interval)
		select {
		case <-stopCh:
			return
		case <-timer.C:
		}
		fn()
	}
}
