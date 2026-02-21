package geoip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockReader is a test GeoReader that returns a fixed country.
type mockReader struct {
	country string
	closed  bool
	mu      sync.Mutex
}

func (m *mockReader) Lookup(_ netip.Addr) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.country
}

func (m *mockReader) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockReader) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// --- Existing tests ---

func TestGeoIP_Lookup_NilReader(t *testing.T) {
	s := &Service{}
	if got := s.Lookup(netip.MustParseAddr("1.2.3.4")); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestNewService_Defaults(t *testing.T) {
	s := NewService(ServiceConfig{
		CacheDir: t.TempDir(),
		OpenDB:   NoOpOpen,
	})
	defer s.Stop()

	if s.dbFilename != "country.mmdb" {
		t.Fatalf("dbFilename = %q, want %q", s.dbFilename, "country.mmdb")
	}

	entry := s.cron.Entry(s.cronEntryID)
	if entry.ID == 0 || entry.Schedule == nil {
		t.Fatal("default cron entry is not configured")
	}

	base := time.Date(2026, 1, 2, 6, 30, 0, 0, time.Local)
	next := entry.Schedule.Next(base)
	want := time.Date(2026, 1, 2, 7, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("next schedule = %v, want %v", next, want)
	}
}

func TestGeoIP_ReloadReader(t *testing.T) {
	old := &mockReader{country: "us"}
	s := &Service{reader: old}

	newReader := &mockReader{country: "jp"}
	s.openDB = func(path string) (GeoReader, error) { return newReader, nil }

	if err := s.reloadReader("/fake/path"); err != nil {
		t.Fatal(err)
	}

	if got := s.Lookup(netip.Addr{}); got != "jp" {
		t.Fatalf("expected jp, got %q", got)
	}
	if !old.isClosed() {
		t.Fatal("old reader should be closed")
	}
}

func TestGeoIP_Stop_ClosesReader(t *testing.T) {
	r := &mockReader{country: "cn"}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	s := &Service{
		reader:     r,
		cron:       nil, // no cron for this test
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
	}
	s.Stop()

	if !r.isClosed() {
		t.Fatal("reader should be closed after stop")
	}
	if got := s.Lookup(netip.Addr{}); got != "" {
		t.Fatalf("expected empty after stop, got %q", got)
	}
}

func TestGeoIP_ConcurrentLookupDuringReload(t *testing.T) {
	initial := &mockReader{country: "us"}
	s := &Service{reader: initial}
	s.openDB = func(path string) (GeoReader, error) {
		return &mockReader{country: "jp"}, nil
	}

	var wg sync.WaitGroup
	// Concurrent lookups.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := s.Lookup(netip.MustParseAddr("1.2.3.4"))
			if got != "us" && got != "jp" {
				t.Errorf("unexpected country: %q", got)
			}
		}()
	}

	// Concurrent reload.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.reloadReader("/fake")
	}()

	wg.Wait()
}

func TestVerifySHA256_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	data := []byte("hello world")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	// SHA256("hello world") = b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9
	if err := VerifySHA256(path, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifySHA256_Failure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := VerifySHA256(path, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("expected SHA256 mismatch error")
	}
}

// --- New download chain tests ---

// mockDownloader records downloads and serves canned responses.
type mockDownloader struct {
	mu        sync.Mutex
	responses map[string][]byte
	calls     []string
}

func (d *mockDownloader) Download(_ context.Context, url string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, url)
	body, ok := d.responses[url]
	if !ok {
		return nil, fmt.Errorf("mock: not found: %s", url)
	}
	return body, nil
}

func TestUpdateNow_DownloadVerifyReload(t *testing.T) {
	dir := t.TempDir()

	// Prepare fake database content.
	dbContent := []byte("fake-geoip-database-content")
	hash := sha256.Sum256(dbContent)
	hashHex := hex.EncodeToString(hash[:])
	digest := "sha256:" + hashHex

	// Build mock release JSON.
	release := releaseInfo{
		TagName: "v20240101",
		Assets: []releaseAsset{
			{Name: "geoip.db", Digest: &digest, BrowserDownloadURL: "https://example.com/geoip.db"},
		},
	}
	releaseJSON, _ := json.Marshal(release)

	dl := &mockDownloader{
		responses: map[string][]byte{
			ReleaseAPIURL:                  releaseJSON,
			"https://example.com/geoip.db": dbContent,
		},
	}

	var reloaded bool
	s := &Service{
		cacheDir:   dir,
		dbFilename: "geoip.db",
		downloader: dl,
		openDB: func(path string) (GeoReader, error) {
			reloaded = true
			return &mockReader{country: "us"}, nil
		},
	}

	if err := s.UpdateNow(); err != nil {
		t.Fatalf("UpdateNow: %v", err)
	}

	// Verify the file was written.
	dbPath := filepath.Join(dir, "geoip.db")
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if string(data) != string(dbContent) {
		t.Fatal("database content mismatch")
	}

	// Verify reader was reloaded.
	if !reloaded {
		t.Fatal("reader was not reloaded after download")
	}

	// Verify lookup works.
	if got := s.Lookup(netip.MustParseAddr("1.2.3.4")); got != "us" {
		t.Fatalf("expected 'us', got %q", got)
	}
}

func TestUpdateNow_SHA256Mismatch_NoReplace(t *testing.T) {
	dir := t.TempDir()

	// Pre-existing database.
	origContent := []byte("original-db")
	dbPath := filepath.Join(dir, "geoip.db")
	if err := os.WriteFile(dbPath, origContent, 0644); err != nil {
		t.Fatal(err)
	}

	// New download content with wrong hash.
	newContent := []byte("new-db-content")
	badDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	release := releaseInfo{
		TagName: "v20240102",
		Assets: []releaseAsset{
			{Name: "geoip.db", Digest: &badDigest, BrowserDownloadURL: "https://example.com/geoip.db"},
		},
	}
	releaseJSON, _ := json.Marshal(release)

	dl := &mockDownloader{
		responses: map[string][]byte{
			ReleaseAPIURL:                  releaseJSON,
			"https://example.com/geoip.db": newContent,
		},
	}

	s := &Service{
		cacheDir:   dir,
		dbFilename: "geoip.db",
		downloader: dl,
		openDB: func(path string) (GeoReader, error) {
			t.Fatal("OpenDB should not be called on SHA256 mismatch")
			return nil, nil
		},
	}

	err := s.UpdateNow()
	if err == nil {
		t.Fatal("expected error on SHA256 mismatch")
	}

	// Original file should be untouched.
	data, rErr := os.ReadFile(dbPath)
	if rErr != nil {
		t.Fatalf("read db: %v", rErr)
	}
	if string(data) != string(origContent) {
		t.Fatal("original database was corrupted despite SHA256 mismatch")
	}
}

func TestUpdateNow_NoDownloader(t *testing.T) {
	s := &Service{
		cacheDir:   t.TempDir(),
		dbFilename: "geoip.db",
		// no downloader
	}
	if err := s.UpdateNow(); err == nil {
		t.Fatal("expected error when no downloader configured")
	}
}

// TestUpdateNow_MissingDigest verifies that UpdateNow errors when the
// release asset does not include a digest (mandatory verification).
func TestUpdateNow_MissingDigest(t *testing.T) {
	dir := t.TempDir()

	// Pre-existing database.
	origContent := []byte("original-db")
	dbPath := filepath.Join(dir, "geoip.db")
	if err := os.WriteFile(dbPath, origContent, 0644); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("new-db-content")

	release := releaseInfo{
		TagName: "v20240103",
		Assets: []releaseAsset{
			// Only .db asset, NO digest.
			{Name: "geoip.db", BrowserDownloadURL: "https://example.com/geoip.db"},
		},
	}
	releaseJSON, _ := json.Marshal(release)

	dl := &mockDownloader{
		responses: map[string][]byte{
			ReleaseAPIURL:                  releaseJSON,
			"https://example.com/geoip.db": newContent,
		},
	}

	s := &Service{
		cacheDir:   dir,
		dbFilename: "geoip.db",
		downloader: dl,
		openDB: func(path string) (GeoReader, error) {
			t.Fatal("OpenDB should not be called when digest is missing")
			return nil, nil
		},
	}

	err := s.UpdateNow()
	if err == nil {
		t.Fatal("expected error when digest is missing")
	}

	// Verify error message mentions missing digest.
	if !strings.Contains(err.Error(), "missing valid sha256 digest") {
		t.Fatalf("expected missing digest error, got: %v", err)
	}

	// Original file should be untouched.
	data, rErr := os.ReadFile(dbPath)
	if rErr != nil {
		t.Fatalf("read db: %v", rErr)
	}
	if string(data) != string(origContent) {
		t.Fatal("original database was corrupted despite missing digest")
	}
}

type notifyDownloader struct {
	called chan struct{}
}

func (d *notifyDownloader) Download(_ context.Context, _ string) ([]byte, error) {
	select {
	case d.called <- struct{}{}:
	default:
	}
	return nil, fmt.Errorf("mock download failure")
}

type blockingDownloader struct {
	started chan struct{}
	release chan struct{}
}

func (d *blockingDownloader) Download(_ context.Context, _ string) ([]byte, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-d.release
	return nil, fmt.Errorf("blocked download failure")
}

func TestGeoIPStart_StatUnexpectedError(t *testing.T) {
	s := NewService(ServiceConfig{
		CacheDir:   t.TempDir(),
		DBFilename: "bad\x00name",
		OpenDB:     NoOpOpen,
	})
	defer s.Stop()

	err := s.Start()
	if err == nil {
		t.Fatal("expected Start to fail on unexpected stat error")
	}
	if !strings.Contains(err.Error(), "stat db") {
		t.Fatalf("expected stat error context, got: %v", err)
	}
}

func TestGeoIPStart_MissingDBTriggersBackgroundUpdate(t *testing.T) {
	dl := &notifyDownloader{called: make(chan struct{}, 1)}
	s := NewService(ServiceConfig{
		CacheDir:   t.TempDir(),
		DBFilename: "geoip.db",
		OpenDB:     NoOpOpen,
		Downloader: dl,
	})
	defer s.Stop()

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	select {
	case <-dl.called:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected background update attempt when db is missing")
	}
}

func TestGeoIPStop_WaitsInFlightUpdateAndClearsReader(t *testing.T) {
	old := &mockReader{country: "us"}
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	downloader := &blockingDownloader{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	s := &Service{
		reader:     old,
		cron:       nil,
		downloader: downloader,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- s.UpdateNow()
	}()

	select {
	case <-downloader.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("UpdateNow did not start download in time")
	}

	stopDone := make(chan struct{})
	go func() {
		s.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight UpdateNow completed")
	case <-time.After(100 * time.Millisecond):
		// expected: Stop is waiting for UpdateNow/updateMu
	}

	close(downloader.release)
	if err := <-updateDone; err == nil {
		t.Fatal("expected UpdateNow to fail from blocked downloader")
	}

	select {
	case <-stopDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not return after in-flight UpdateNow finished")
	}

	if got := s.Lookup(netip.MustParseAddr("1.2.3.4")); got != "" {
		t.Fatalf("expected empty lookup after Stop, got %q", got)
	}
	if !old.isClosed() {
		t.Fatal("reader should be closed after Stop")
	}
}

func TestUpdateNow_AfterStopReturnsCanceled(t *testing.T) {
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	downloader := &notifyDownloader{called: make(chan struct{}, 1)}
	s := &Service{
		cacheDir:   t.TempDir(),
		dbFilename: "geoip.db",
		cron:       nil,
		downloader: downloader,
		openDB:     NoOpOpen,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
	}

	s.Stop()

	err := s.UpdateNow()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	select {
	case <-downloader.called:
		t.Fatal("downloader should not be called after Stop")
	default:
	}
}

func TestParseSHA256Digest(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
		{"SHA256:B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
		{"sha512:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", ""},
		{"sha256:abc", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := parseSHA256Digest(tt.input)
		if got != tt.want {
			t.Errorf("parseSHA256Digest(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
