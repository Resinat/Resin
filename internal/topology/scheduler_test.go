package topology

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/resin-proxy/resin/internal/netutil"
	"github.com/resin-proxy/resin/internal/node"
	"github.com/resin-proxy/resin/internal/platform"
	"github.com/resin-proxy/resin/internal/subscription"
	"github.com/resin-proxy/resin/internal/testutil"
)

// makeMockFetcher returns a Fetcher that serves the given response.
func makeMockFetcher(body []byte, err error) func(string) ([]byte, error) {
	return func(url string) ([]byte, error) {
		return body, err
	}
}

func makeSubscriptionJSON(outbounds ...string) []byte {
	arr := "["
	for i, o := range outbounds {
		if i > 0 {
			arr += ","
		}
		arr += o
	}
	arr += "]"
	return []byte(`{"outbounds":` + arr + `}`)
}

func newTestScheduler(subMgr *SubscriptionManager, pool *GlobalNodePool, fetcher func(string) ([]byte, error)) *SubscriptionScheduler {
	return NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Fetcher:    fetcher,
	})
}

// --- Test: UpdateSubscription success path ---

func TestScheduler_UpdateSubscription_Success(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	sub.SetFetchConfig(sub.URL(), int64(time.Hour))
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	body := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"us-1","server":"1.1.1.1","server_port":443}`,
		`{"type":"vmess","tag":"jp-1","server":"2.2.2.2","server_port":443}`,
	)
	fetcher := makeMockFetcher(body, nil)
	sched := newTestScheduler(subMgr, pool, fetcher)

	sched.UpdateSubscription(sub)

	// Verify pool has 2 nodes.
	if pool.Size() != 2 {
		t.Fatalf("expected 2 nodes in pool, got %d", pool.Size())
	}

	// Verify ManagedNodes.
	count := 0
	sub.ManagedNodes().Range(func(_ node.Hash, _ []string) bool {
		count++
		return true
	})
	if count != 2 {
		t.Fatalf("expected 2 managed nodes, got %d", count)
	}

	// Verify timestamps updated.
	if sub.LastCheckedNs.Load() == 0 {
		t.Fatal("LastCheckedNs should be set")
	}
	if sub.LastUpdatedNs.Load() == 0 {
		t.Fatal("LastUpdatedNs should be set")
	}
	if sub.GetLastError() != "" {
		t.Fatalf("LastError should be empty, got %s", sub.GetLastError())
	}
}

func TestScheduler_UpdateSubscription_DownloadViaHTTPServer(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	const rawOutbound = `{"type":"shadowsocks","tag":"http-node","server":"1.1.1.1","server_port":443,"method":"aes-256-gcm","password":"secret"}`
	body := makeSubscriptionJSON(rawOutbound)

	subUserAgent := "resin-scheduler-e2e"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != subUserAgent {
			t.Fatalf("user-agent: got %q, want %q", ua, subUserAgent)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	sub := subscription.NewSubscription("s1", "TestSub", srv.URL+"/sub", true, false)
	subMgr.Register(sub)

	downloader := netutil.NewDirectDownloader(
		func() time.Duration { return time.Second },
		func() string { return subUserAgent },
	)
	sched := NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Downloader: downloader,
	})

	sched.UpdateSubscription(sub)

	if sub.GetLastError() != "" {
		t.Fatalf("unexpected last error: %q", sub.GetLastError())
	}
	if sub.LastCheckedNs.Load() == 0 {
		t.Fatal("LastCheckedNs should be set")
	}
	if sub.LastUpdatedNs.Load() == 0 {
		t.Fatal("LastUpdatedNs should be set")
	}

	hash := node.HashFromRawOptions([]byte(rawOutbound))
	if _, ok := sub.ManagedNodes().Load(hash); !ok {
		t.Fatalf("managed nodes should contain %s", hash.Hex())
	}
	if _, ok := pool.GetEntry(hash); !ok {
		t.Fatalf("pool should contain %s", hash.Hex())
	}
}

// --- Test: UpdateSubscription fetch failure ---

func TestScheduler_UpdateSubscription_FetchFailure(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	fetcher := makeMockFetcher(nil, errors.New("network error"))
	sched := newTestScheduler(subMgr, pool, fetcher)

	sched.UpdateSubscription(sub)

	if sub.GetLastError() != "network error" {
		t.Fatalf("expected 'network error', got %q", sub.GetLastError())
	}
	if sub.LastCheckedNs.Load() == 0 {
		t.Fatal("LastCheckedNs should be set on failure")
	}
	if pool.Size() != 0 {
		t.Fatalf("pool should be empty after fetch failure, got %d", pool.Size())
	}
}

// --- Test: UpdateSubscription parse failure ---

func TestScheduler_UpdateSubscription_ParseFailure(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	fetcher := makeMockFetcher([]byte(`not valid json`), nil)
	sched := newTestScheduler(subMgr, pool, fetcher)

	sched.UpdateSubscription(sub)

	if sub.GetLastError() == "" {
		t.Fatal("LastError should be set on parse failure")
	}
	if pool.Size() != 0 {
		t.Fatal("pool should be empty after parse failure")
	}
}

// --- Test: Swap-before-add/remove ordering ---

func TestScheduler_UpdateSubscription_SwapBeforePoolMutation(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	// Preload with initial nodes.
	initialBody := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"node-a","server":"1.1.1.1","server_port":443}`,
	)
	pool := newTestPool(subMgr)
	sched := newTestScheduler(subMgr, pool, makeMockFetcher(initialBody, nil))
	sched.UpdateSubscription(sub)

	oldHash := node.HashFromRawOptions([]byte(`{"type":"shadowsocks","tag":"node-a","server":"1.1.1.1","server_port":443}`))

	// Now update to different nodes.
	newBody := makeSubscriptionJSON(
		`{"type":"vmess","tag":"node-b","server":"2.2.2.2","server_port":443}`,
	)
	sched.Fetcher = makeMockFetcher(newBody, nil)
	sched.UpdateSubscription(sub)

	newHash := node.HashFromRawOptions([]byte(`{"type":"vmess","tag":"node-b","server":"2.2.2.2","server_port":443}`))

	// Old node should be removed, new node should be added.
	if _, ok := pool.GetEntry(oldHash); ok {
		t.Fatal("old node should have been removed from pool")
	}
	if _, ok := pool.GetEntry(newHash); !ok {
		t.Fatal("new node should be in pool")
	}

	// ManagedNodes should only have new hash.
	if _, ok := sub.ManagedNodes().Load(oldHash); ok {
		t.Fatal("old hash should not be in ManagedNodes")
	}
	if _, ok := sub.ManagedNodes().Load(newHash); !ok {
		t.Fatal("new hash should be in ManagedNodes")
	}
}

// --- Test: Idempotent update (same data) ---

func TestScheduler_UpdateSubscription_Idempotent(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	body := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"us-1","server":"1.1.1.1","server_port":443}`,
	)
	sched := newTestScheduler(subMgr, pool, makeMockFetcher(body, nil))

	// Update three times with same data.
	sched.UpdateSubscription(sub)
	sched.UpdateSubscription(sub)
	sched.UpdateSubscription(sub)

	if pool.Size() != 1 {
		t.Fatalf("expected 1 node after idempotent updates, got %d", pool.Size())
	}

	h := node.HashFromRawOptions([]byte(`{"type":"shadowsocks","tag":"us-1","server":"1.1.1.1","server_port":443}`))
	entry, _ := pool.GetEntry(h)
	if entry.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 sub ref, got %d", entry.SubscriptionCount())
	}
}

// --- Test: Rename triggers re-filter ---

func TestScheduler_RenameSubscription(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "OldName", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	body := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"us-1","server":"1.1.1.1","server_port":443}`,
	)
	sched := newTestScheduler(subMgr, pool, makeMockFetcher(body, nil))
	sched.UpdateSubscription(sub)

	// Rename should update name and re-add all hashes (triggering platform re-filter).
	sched.RenameSubscription(sub, "NewName")

	if sub.Name() != "NewName" {
		t.Fatalf("expected NewName, got %s", sub.Name())
	}

	// Pool should still have 1 node.
	if pool.Size() != 1 {
		t.Fatalf("expected 1 node after rename, got %d", pool.Size())
	}
}

// --- Test: Failure path is serialized with WithSubLock ---

func TestScheduler_FailurePath_Serialized(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	var fetchCount atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		fetchCount.Add(1)
		return nil, errors.New("fail")
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	// Run concurrent updates — all failure paths should serialize via WithSubLock.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sched.UpdateSubscription(sub)
		}()
	}
	wg.Wait()

	if fetchCount.Load() != 20 {
		t.Fatalf("expected 20 fetch attempts, got %d", fetchCount.Load())
	}
	if sub.GetLastError() != "fail" {
		t.Fatalf("expected 'fail' error, got %q", sub.GetLastError())
	}
}

func TestScheduler_StaleFailureDoesNotOverrideNewerSuccess(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	successBody := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"ok-node","server":"1.1.1.1","server_port":443}`,
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil, errors.New("stale failure")
		}
		return successBody, nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		sched.UpdateSubscription(sub) // older attempt: blocks in fetcher and eventually fails.
	}()

	<-firstStarted

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		sched.UpdateSubscription(sub) // newer attempt: succeeds first.
	}()
	<-done2

	close(releaseFirst)
	<-done1

	if calls.Load() != 2 {
		t.Fatalf("expected 2 fetch calls, got %d", calls.Load())
	}
	if sub.GetLastError() != "" {
		t.Fatalf("stale failure must not overwrite newer success, got error=%q", sub.GetLastError())
	}
	if sub.LastUpdatedNs.Load() == 0 {
		t.Fatal("newer success should set LastUpdatedNs")
	}
	if pool.Size() != 1 {
		t.Fatalf("expected 1 node in pool after newer success, got %d", pool.Size())
	}

	h := node.HashFromRawOptions([]byte(`{"type":"shadowsocks","tag":"ok-node","server":"1.1.1.1","server_port":443}`))
	if _, ok := sub.ManagedNodes().Load(h); !ok {
		t.Fatal("managed nodes should contain hash from newer successful update")
	}
}

func TestScheduler_StaleSuccessDoesNotOverrideNewerSuccess(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	oldBody := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"old-node","server":"1.1.1.1","server_port":443}`,
	)
	newBody := makeSubscriptionJSON(
		`{"type":"vmess","tag":"new-node","server":"2.2.2.2","server_port":443}`,
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return oldBody, nil // older attempt returns stale success
		}
		return newBody, nil // newer attempt succeeds first
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		sched.UpdateSubscription(sub) // older attempt: blocked in fetcher, then stale success
	}()

	<-firstStarted

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		sched.UpdateSubscription(sub) // newer attempt: success lands first
	}()
	<-done2

	close(releaseFirst)
	<-done1

	if calls.Load() != 2 {
		t.Fatalf("expected 2 fetch calls, got %d", calls.Load())
	}
	if sub.GetLastError() != "" {
		t.Fatalf("expected empty last error, got %q", sub.GetLastError())
	}

	// Newer success should win; stale old success must be ignored.
	if pool.Size() != 1 {
		t.Fatalf("expected exactly 1 node in pool, got %d", pool.Size())
	}
	oldHash := node.HashFromRawOptions([]byte(`{"type":"shadowsocks","tag":"old-node","server":"1.1.1.1","server_port":443}`))
	newHash := node.HashFromRawOptions([]byte(`{"type":"vmess","tag":"new-node","server":"2.2.2.2","server_port":443}`))
	if _, ok := pool.GetEntry(newHash); !ok {
		t.Fatal("expected new node to remain after stale-success race")
	}
	if _, ok := pool.GetEntry(oldHash); ok {
		t.Fatal("stale old success should not overwrite newer subscription state")
	}
	if _, ok := sub.ManagedNodes().Load(newHash); !ok {
		t.Fatal("managed nodes should contain new hash")
	}
	if _, ok := sub.ManagedNodes().Load(oldHash); ok {
		t.Fatal("managed nodes should not contain old hash after stale-success race")
	}
}

// --- Test: Concurrent update + rename serialization ---

func TestScheduler_ConcurrentUpdateAndRename(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Name0", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	body := makeSubscriptionJSON(
		`{"type":"shadowsocks","tag":"node","server":"1.1.1.1","server_port":443}`,
	)
	sched := newTestScheduler(subMgr, pool, makeMockFetcher(body, nil))

	var wg sync.WaitGroup
	// Concurrent updates.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sched.UpdateSubscription(sub)
		}()
	}
	// Concurrent renames.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sched.RenameSubscription(sub, fmt.Sprintf("Name%d", n))
		}(i)
	}
	wg.Wait()

	// Pool should have exactly 1 node.
	if pool.Size() != 1 {
		t.Fatalf("expected 1 node after concurrent ops, got %d", pool.Size())
	}

	h := node.HashFromRawOptions([]byte(`{"type":"shadowsocks","tag":"node","server":"1.1.1.1","server_port":443}`))
	entry, _ := pool.GetEntry(h)
	if entry.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 sub ref, got %d", entry.SubscriptionCount())
	}
}

// --- Test: Due check logic ---

func TestScheduler_DueCheck(t *testing.T) {
	subMgr := NewSubscriptionManager()

	// Sub that was checked 2 hours ago with 1-hour interval → due.
	dueSub := subscription.NewSubscription("s1", "Due", "http://example.com", true, false)
	dueSub.SetFetchConfig(dueSub.URL(), int64(time.Hour))
	dueSub.LastCheckedNs.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	subMgr.Register(dueSub)

	// Sub that was just checked → not due.
	notDueSub := subscription.NewSubscription("s2", "NotDue", "http://example.com", true, false)
	notDueSub.SetFetchConfig(notDueSub.URL(), int64(time.Hour))
	notDueSub.LastCheckedNs.Store(time.Now().UnixNano())
	subMgr.Register(notDueSub)

	pool := newTestPool(subMgr)
	var fetchedURLs sync.Map
	fetcher := func(url string) ([]byte, error) {
		fetchedURLs.Store(url, true)
		return makeSubscriptionJSON(), nil
	}
	_ = newTestScheduler(subMgr, pool, fetcher)

	// Simulate the due check from scheduler.run().
	now := time.Now().UnixNano()
	var dueIDs []string
	subMgr.Range(func(id string, sub *subscription.Subscription) bool {
		if !sub.Enabled() {
			return true
		}
		if sub.LastCheckedNs.Load()+sub.UpdateIntervalNs()-15*int64(time.Second) <= now {
			dueIDs = append(dueIDs, id)
		}
		return true
	})

	if len(dueIDs) != 1 {
		t.Fatalf("expected 1 due sub, got %d: %v", len(dueIDs), dueIDs)
	}
	if dueIDs[0] != "s1" {
		t.Fatalf("expected s1 to be due, got %s", dueIDs[0])
	}
}

func TestScheduler_Tick_UpdatesDueSubscriptionsInParallel(t *testing.T) {
	subMgr := NewSubscriptionManager()

	due1 := subscription.NewSubscription("s1", "Due1", "http://example.com/due1", true, false)
	due1.SetFetchConfig(due1.URL(), int64(time.Hour))
	due1.LastCheckedNs.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	subMgr.Register(due1)

	due2 := subscription.NewSubscription("s2", "Due2", "http://example.com/due2", true, false)
	due2.SetFetchConfig(due2.URL(), int64(time.Hour))
	due2.LastCheckedNs.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	subMgr.Register(due2)

	notDue := subscription.NewSubscription("s3", "NotDue", "http://example.com/not-due", true, false)
	notDue.SetFetchConfig(notDue.URL(), int64(time.Hour))
	notDue.LastCheckedNs.Store(time.Now().UnixNano())
	subMgr.Register(notDue)

	pool := newTestPool(subMgr)
	releaseFetch := make(chan struct{})
	allStarted := make(chan struct{})
	var started atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		if started.Add(1) == 2 {
			close(allStarted)
		}
		<-releaseFetch
		return makeSubscriptionJSON(), nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	done := make(chan struct{})
	go func() {
		sched.tick()
		close(done)
	}()

	select {
	case <-allStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected due subscription refreshes to start in parallel")
	}

	select {
	case <-done:
		t.Fatal("tick should wait for in-flight due refreshes")
	default:
	}

	close(releaseFetch)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tick did not finish after fetchers were released")
	}

	if got := started.Load(); got != 2 {
		t.Fatalf("expected 2 fetch attempts for due subscriptions, got %d", got)
	}
}

func TestScheduler_ForceRefreshAllAsync_ReturnsImmediately(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Async", "http://example.com/async", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	fetcher := func(url string) ([]byte, error) {
		close(fetchStarted)
		<-releaseFetch
		return makeSubscriptionJSON(), nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	returned := make(chan struct{})
	go func() {
		sched.ForceRefreshAllAsync()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ForceRefreshAllAsync should return immediately")
	}

	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}

	close(releaseFetch)
	sched.Stop()
}

func TestScheduler_ForceRefreshAll_UpdatesSubscriptionsInParallel(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub1 := subscription.NewSubscription("s1", "One", "http://example.com/one", true, false)
	sub2 := subscription.NewSubscription("s2", "Two", "http://example.com/two", true, false)
	subMgr.Register(sub1)
	subMgr.Register(sub2)

	pool := newTestPool(subMgr)
	releaseFetch := make(chan struct{})
	allStarted := make(chan struct{})
	var started atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		if started.Add(1) == 2 {
			close(allStarted)
		}
		<-releaseFetch
		return makeSubscriptionJSON(), nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	done := make(chan struct{})
	go func() {
		sched.ForceRefreshAll()
		close(done)
	}()

	select {
	case <-allStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("expected both subscription refreshes to start in parallel")
	}

	select {
	case <-done:
		t.Fatal("ForceRefreshAll should wait for in-flight refreshes")
	default:
	}

	close(releaseFetch)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ForceRefreshAll did not finish after fetchers were released")
	}

	if got := started.Load(); got != 2 {
		t.Fatalf("expected 2 fetch attempts, got %d", got)
	}
}

func TestScheduler_ForceRefreshAll_AfterStopDoesNotFetch(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "One", "http://example.com/one", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	var calls atomic.Int32
	fetcher := func(url string) ([]byte, error) {
		calls.Add(1)
		return makeSubscriptionJSON(), nil
	}
	sched := newTestScheduler(subMgr, pool, fetcher)

	sched.Stop()
	sched.ForceRefreshAll()

	if got := calls.Load(); got != 0 {
		t.Fatalf("expected no fetch attempts after stop, got %d", got)
	}
}

func TestScheduler_StopCancelsInFlightForceRefreshDownload(t *testing.T) {
	subMgr := NewSubscriptionManager()

	requestStarted := make(chan struct{})
	requestDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestDone)
	}))
	defer srv.Close()

	sub := subscription.NewSubscription("s1", "One", srv.URL, true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	downloader := netutil.NewDirectDownloader(
		func() time.Duration { return 30 * time.Second },
		func() string { return "resin-scheduler-stop-test" },
	)
	sched := NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Downloader: downloader,
	})

	sched.ForceRefreshAllAsync()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("expected in-flight download to start")
	}

	stopped := make(chan struct{})
	go func() {
		sched.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler Stop should cancel in-flight download and return promptly")
	}

	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("expected HTTP handler context to be canceled after scheduler stop")
	}
}

// --- Test: Persistence callback invoked ---

func TestScheduler_OnSubUpdated_Called(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "TestSub", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	body := makeSubscriptionJSON(`{"type":"shadowsocks","tag":"n","server":"1.1.1.1","server_port":443}`)

	var callCount atomic.Int32
	sched := NewSubscriptionScheduler(SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
		Fetcher:    makeMockFetcher(body, nil),
		OnSubUpdated: func(s *subscription.Subscription) {
			callCount.Add(1)
		},
	})

	sched.UpdateSubscription(sub)
	if callCount.Load() != 1 {
		t.Fatalf("expected onSubUpdated to be called once, got %d", callCount.Load())
	}

	// Also on failure.
	sched.Fetcher = makeMockFetcher(nil, errors.New("fail"))
	sched.UpdateSubscription(sub)
	if callCount.Load() != 2 {
		t.Fatalf("expected onSubUpdated to be called on failure too, got %d", callCount.Load())
	}
}

// --- Test: Disabled ephemeral sub still gets evicted ---

func TestEphemeralCleaner_DisabledEphemeralStillEvicted(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "EphSub", "url", false, true) // disabled + ephemeral
	subMgr.Register(sub)

	pool := newTestPool(subMgr)
	raw := json.RawMessage(`{"type":"ss","server":"1.1.1.1"}`)
	h := node.HashFromRawOptions(raw)

	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"node-1"})
	sub.SwapManagedNodes(mn)
	pool.AddNodeFromSub(h, raw, "s1")

	entry, _ := pool.GetEntry(h)
	entry.CircuitOpenSince.Store(time.Now().Add(-2 * time.Minute).UnixNano())

	cleaner := NewEphemeralCleaner(subMgr, pool, func() time.Duration { return 1 * time.Minute })
	cleaner.sweep()

	// Disabled ephemeral sub should still be cleaned.
	if pool.Size() != 0 {
		t.Fatal("disabled ephemeral sub should still have circuit-broken nodes evicted")
	}
}

// --- Test: ReadOnlyView compile-time constraint ---

func TestPlatform_ReadOnlyView_Interface(t *testing.T) {
	plat := platform.NewPlatform("p1", "Test", nil, nil)

	// View() returns ReadOnlyView, not *RoutableView.
	// This compile-time type assignment verifies the interface constraint.
	var view platform.ReadOnlyView = plat.View()

	// Read methods work.
	if view.Size() != 0 {
		t.Fatalf("empty view should have size 0, got %d", view.Size())
	}
	if view.Contains(node.Hash{}) {
		t.Fatal("empty view should not contain anything")
	}

	// The key constraint: callers with only a ReadOnlyView cannot call
	// Add/Remove/Clear — there are no such methods on the interface.
	// This is enforced at compile time, not runtime.
	// (Go's type assertion can still recover the concrete type, but API
	// consumers using the interface type cannot accidentally mutate.)
	t.Log("ReadOnlyView interface correctly restricts mutation methods at the type level")
}

// Ensure ReadOnlyView is correctly implemented by RoutableView.
var _ platform.ReadOnlyView = (*platform.RoutableView)(nil)

// --- Test: SetSubscriptionEnabled rebuilds platform views ---

func TestScheduler_SetSubscriptionEnabled_RebuildsPlatformViews(t *testing.T) {
	subMgr := NewSubscriptionManager()
	sub := subscription.NewSubscription("s1", "Provider", "http://example.com", true, false)
	subMgr.Register(sub)

	pool := newTestPool(subMgr)

	// Platform with regex matching the tag "us-node" via sub name "Provider".
	plat := platform.NewPlatform("p1", "US", []*regexp.Regexp{regexp.MustCompile("us")}, nil)
	pool.RegisterPlatform(plat)

	raw := json.RawMessage(`{"type":"shadowsocks","server":"1.1.1.1","server_port":443}`)
	h := node.HashFromRawOptions(raw)

	// Set up managed nodes and make the node fully routable.
	mn := xsync.NewMap[node.Hash, []string]()
	mn.Store(h, []string{"us-node"})
	sub.SwapManagedNodes(mn)

	pool.AddNodeFromSub(h, raw, "s1")
	entry, _ := pool.GetEntry(h)
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))

	// Trigger rebuild so node appears in view.
	pool.RebuildAllPlatforms()
	if plat.View().Size() != 1 {
		t.Fatalf("expected node in view while enabled, got %d", plat.View().Size())
	}

	// Create scheduler for SetSubscriptionEnabled.
	sched := newTestScheduler(subMgr, pool, nil)

	// Disable → node should disappear from platform view.
	sched.SetSubscriptionEnabled(sub, false)
	if sub.Enabled() {
		t.Fatal("sub should be disabled")
	}
	if plat.View().Size() != 0 {
		t.Fatalf("expected 0 nodes in view after disable, got %d", plat.View().Size())
	}

	// Re-enable → node should reappear.
	sched.SetSubscriptionEnabled(sub, true)
	if !sub.Enabled() {
		t.Fatal("sub should be re-enabled")
	}
	if plat.View().Size() != 1 {
		t.Fatalf("expected node in view after re-enable, got %d", plat.View().Size())
	}
}
