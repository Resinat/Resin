package routing

import (
	"testing"
	"time"

	"github.com/resin-proxy/resin/internal/platform"
)

func TestLeaseCleaner_StopWaitsForInFlightSweep(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-stop", "Plat-Stop", nil, nil)
	pool.addPlatform(plat)
	router := newTestRouter(pool, nil)

	cleaner := newLeaseCleanerWithIntervals(router, time.Millisecond, 0)

	started := make(chan struct{})
	release := make(chan struct{})
	cleaner.sweepHook = func() {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
	}

	cleaner.Start()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sweep did not start in time")
	}

	stopDone := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight sweep completed")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)

	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after in-flight sweep completed")
	}
}
