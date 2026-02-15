package geoip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	s := &Service{
		reader: r,
		cron:   nil, // no cron for this test
	}
	// Manually close.
	s.mu.Lock()
	reader := s.reader
	s.reader = nil
	s.mu.Unlock()
	if reader != nil {
		reader.Close()
	}

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

	// Build mock release JSON.
	release := releaseInfo{
		TagName: "v20240101",
		Assets: []releaseAsset{
			{Name: "geoip.db", BrowserDownloadURL: "https://example.com/geoip.db"},
			{Name: "geoip.db.sha256sum", BrowserDownloadURL: "https://example.com/geoip.db.sha256sum"},
		},
	}
	releaseJSON, _ := json.Marshal(release)

	dl := &mockDownloader{
		responses: map[string][]byte{
			ReleaseAPIURL:                            releaseJSON,
			"https://example.com/geoip.db":           dbContent,
			"https://example.com/geoip.db.sha256sum": []byte(hashHex + "  geoip.db\n"),
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

	release := releaseInfo{
		TagName: "v20240102",
		Assets: []releaseAsset{
			{Name: "geoip.db", BrowserDownloadURL: "https://example.com/geoip.db"},
			{Name: "geoip.db.sha256sum", BrowserDownloadURL: "https://example.com/geoip.db.sha256sum"},
		},
	}
	releaseJSON, _ := json.Marshal(release)

	dl := &mockDownloader{
		responses: map[string][]byte{
			ReleaseAPIURL:                            releaseJSON,
			"https://example.com/geoip.db":           newContent,
			"https://example.com/geoip.db.sha256sum": []byte("0000000000000000000000000000000000000000000000000000000000000000  geoip.db\n"),
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

// TestUpdateNow_MissingSHA256Asset verifies that UpdateNow errors when the
// release does not contain a .sha256sum asset (mandatory verification).
func TestUpdateNow_MissingSHA256Asset(t *testing.T) {
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
			// Only .db asset, NO .sha256sum
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
			t.Fatal("OpenDB should not be called when sha256sum is missing")
			return nil, nil
		},
	}

	err := s.UpdateNow()
	if err == nil {
		t.Fatal("expected error when .sha256sum asset is missing")
	}

	// Verify error message mentions refusal.
	if !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("expected 'refusing to replace' in error, got: %v", err)
	}

	// Original file should be untouched.
	data, rErr := os.ReadFile(dbPath)
	if rErr != nil {
		t.Fatalf("read db: %v", rErr)
	}
	if string(data) != string(origContent) {
		t.Fatal("original database was corrupted despite missing sha256sum")
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

func TestParseSHA256Sum(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9  geoip.db\n", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
		{"B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9  file.db", "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"},
		{"abc", ""}, // too short
		{"", ""},    // empty
	}
	for _, tt := range tests {
		got := parseSHA256Sum(tt.input)
		if got != tt.want {
			t.Errorf("parseSHA256Sum(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
