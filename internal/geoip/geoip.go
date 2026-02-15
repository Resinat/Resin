package geoip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	sgGeoip "github.com/sagernet/sing-box/common/geoip"

	"github.com/resin-proxy/resin/internal/netutil"
)

// GeoReader abstracts the GeoIP database reader (e.g., sing-box geoip.Reader).
// This interface allows different implementations and simplifies testing.
type GeoReader interface {
	Lookup(ip netip.Addr) string
	Close() error
}

// OpenFunc opens a GeoIP database file and returns a GeoReader.
// For sing-box: func(path string) (GeoReader, error) { r, _, err := geoip.Open(path); return r, err }
type OpenFunc func(path string) (GeoReader, error)

// noOpReader is a placeholder reader that returns "" for all lookups.
// Used until the real sing-geoip dependency is integrated.
type noOpReader struct{}

func (noOpReader) Lookup(_ netip.Addr) string { return "" }
func (noOpReader) Close() error               { return nil }

// NoOpOpen is a placeholder OpenFunc for tests. Always returns a reader
// that returns empty string.
func NoOpOpen(_ string) (GeoReader, error) { return noOpReader{}, nil }

// SingBoxOpen opens a sing-geoip mmdb database using sing-box's geoip.Reader.
// This is the production OpenFunc.
func SingBoxOpen(path string) (GeoReader, error) {
	reader, _, err := sgGeoip.Open(path)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

// ServiceConfig configures the GeoIP service.
type ServiceConfig struct {
	CacheDir       string             // directory where geoip.db is stored
	DBFilename     string             // default "geoip.db"
	UpdateSchedule string             // cron expression, default "0 5 12 * *"
	OpenDB         OpenFunc           // function to open the database
	Downloader     netutil.Downloader // shared downloader for fetching releases
}

// ReleaseAPIURL is the GitHub API endpoint for the latest sing-geoip release.
const ReleaseAPIURL = "https://api.github.com/repos/SagerNet/sing-geoip/releases/latest"

// Service provides GeoIP lookup with hot-reloading via RWMutex.
type Service struct {
	mu     sync.RWMutex
	reader GeoReader // nil until first load

	cacheDir    string
	dbFilename  string
	openDB      OpenFunc
	downloader  netutil.Downloader
	cron        *cron.Cron
	cronEntryID cron.EntryID
	updateMu    sync.Mutex // serializes UpdateNow calls
	lifeCtx     context.Context
	lifeCancel  context.CancelFunc
}

// NewService creates a new GeoIP service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.DBFilename == "" {
		cfg.DBFilename = "geoip.db"
	}
	if cfg.UpdateSchedule == "" {
		cfg.UpdateSchedule = "0 5 12 * *"
	}
	c := cron.New()
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	s := &Service{
		cacheDir:   cfg.CacheDir,
		dbFilename: cfg.DBFilename,
		openDB:     cfg.OpenDB,
		downloader: cfg.Downloader,
		cron:       c,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
	}

	// Schedule periodic updates.
	entryID, err := c.AddFunc(cfg.UpdateSchedule, func() {
		if err := s.UpdateNow(); err != nil {
			log.Printf("[geoip] scheduled update failed: %v", err)
		}
	})
	if err != nil {
		log.Printf("[geoip] invalid cron expression %q: %v", cfg.UpdateSchedule, err)
	} else {
		s.cronEntryID = entryID
	}

	return s
}

// Start loads the initial database (if present), checks for staleness
// against the cron schedule, and starts the cron scheduler.
func (s *Service) Start() error {
	dbPath := filepath.Join(s.cacheDir, s.dbFilename)
	info, err := os.Stat(dbPath)
	if err == nil {
		// Load existing database.
		if err := s.reloadReader(dbPath); err != nil {
			log.Printf("[geoip] failed to load initial db: %v", err)
		}

		// Check staleness: if mtime is older than the scheduled interval,
		// trigger an immediate background update.
		if s.isStale(info.ModTime()) {
			log.Println("[geoip] database is stale, triggering background update")
			go func() {
				if err := s.UpdateNow(); err != nil {
					log.Printf("[geoip] startup update failed: %v", err)
				}
			}()
		}
	} else if os.IsNotExist(err) {
		// No local database at all — download immediately in background.
		log.Println("[geoip] no local database found, triggering background download")
		go func() {
			if err := s.UpdateNow(); err != nil {
				log.Printf("[geoip] initial download failed: %v", err)
			}
		}()
	} else {
		return fmt.Errorf("geoip: stat db %s: %w", dbPath, err)
	}
	s.cron.Start()
	return nil
}

// isStale returns true if the file's mtime is older than the expected
// cron schedule interval. Uses 2× the gap between two consecutive cron
// firings to tolerate jitter. Falls back to 32 days if the schedule
// cannot be determined.
func (s *Service) isStale(modTime time.Time) bool {
	entry := s.cron.Entry(s.cronEntryID)
	if entry.ID == 0 || entry.Schedule == nil {
		// Cron not configured — fall back to conservative default.
		return time.Since(modTime) > 32*24*time.Hour
	}

	// Compute the gap between two consecutive firings.
	now := time.Now()
	next := entry.Schedule.Next(now)
	nextNext := entry.Schedule.Next(next)
	interval := nextNext.Sub(next)
	if interval <= 0 {
		interval = 32 * 24 * time.Hour
	}

	// Stale if mtime is older than 2× the interval.
	return time.Since(modTime) > 2*interval
}

// Stop stops the cron scheduler and closes the reader.
func (s *Service) Stop() {
	if s.lifeCancel != nil {
		s.lifeCancel()
	}
	s.cron.Stop()
	s.mu.Lock()
	r := s.reader
	s.reader = nil
	s.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// Lookup returns the country code for the given IP address.
// Thread-safe: holds RLock for the entire duration of the lookup.
func (s *Service) Lookup(ip netip.Addr) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.reader == nil {
		return ""
	}
	return s.reader.Lookup(ip)
}

// releaseAsset represents a GitHub release asset.
type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// releaseInfo represents a GitHub release.
type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// UpdateNow downloads the latest GeoIP database from GitHub, verifies SHA256,
// atomically replaces the local file, and hot-reloads the reader.
// Serialized via updateMu to prevent concurrent temp file races.
func (s *Service) UpdateNow() error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	if s.downloader == nil {
		return fmt.Errorf("geoip: no downloader configured")
	}

	parent := context.Background()
	if s.lifeCtx != nil {
		parent = s.lifeCtx
	}
	ctx := parent

	// 1. Fetch latest release metadata.
	releaseBody, err := s.downloader.Download(ctx, ReleaseAPIURL)
	if err != nil {
		return fmt.Errorf("geoip: fetch release info: %w", err)
	}

	var release releaseInfo
	if err := json.Unmarshal(releaseBody, &release); err != nil {
		return fmt.Errorf("geoip: parse release info: %w", err)
	}

	// 2. Find the .db and .db.sha256sum asset URLs.
	dbURL, sha256URL := "", ""
	for _, a := range release.Assets {
		if a.Name == s.dbFilename {
			dbURL = a.BrowserDownloadURL
		} else if a.Name == s.dbFilename+".sha256sum" {
			sha256URL = a.BrowserDownloadURL
		}
	}
	if dbURL == "" {
		return fmt.Errorf("geoip: asset %q not found in release %s", s.dbFilename, release.TagName)
	}

	// 3. Download .db to unique temp file.
	dbData, err := s.downloader.Download(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("geoip: download db: %w", err)
	}

	tmpFile, err := os.CreateTemp(s.cacheDir, s.dbFilename+".tmp.*")
	if err != nil {
		return fmt.Errorf("geoip: create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(dbData); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("geoip: write temp: %w", err)
	}
	tmpFile.Close()
	// Clean up temp on any error after this point.
	defer func() {
		os.Remove(tmpPath) // no-op if already renamed
	}()

	// 4. Verify SHA256 — mandatory.
	if sha256URL == "" {
		return fmt.Errorf("geoip: sha256sum asset %q not found in release %s; refusing to replace without verification",
			s.dbFilename+".sha256sum", release.TagName)
	}
	sha256Body, err := s.downloader.Download(ctx, sha256URL)
	if err != nil {
		return fmt.Errorf("geoip: download sha256: %w", err)
	}
	expectedHash := parseSHA256Sum(string(sha256Body))
	if expectedHash == "" {
		return fmt.Errorf("geoip: could not parse sha256sum from %q", string(sha256Body))
	}
	if err := VerifySHA256(tmpPath, expectedHash); err != nil {
		return err
	}

	// 5. Atomic rename.
	dbPath := filepath.Join(s.cacheDir, s.dbFilename)
	if err := os.Rename(tmpPath, dbPath); err != nil {
		return fmt.Errorf("geoip: atomic replace: %w", err)
	}

	// 6. Hot-reload reader.
	return s.reloadReader(dbPath)
}

// reloadReader atomically replaces the current reader with a new one.
// Safe: RLock holders finish before old reader is closed.
func (s *Service) reloadReader(path string) error {
	if s.openDB == nil {
		return fmt.Errorf("geoip: no OpenDB function configured")
	}
	newReader, err := s.openDB(path)
	if err != nil {
		return fmt.Errorf("geoip: open %s: %w", path, err)
	}
	s.mu.Lock()
	old := s.reader
	s.reader = newReader
	s.mu.Unlock()
	// Safe to close old: all RLock holders on old have released.
	if old != nil {
		old.Close()
	}
	return nil
}

// VerifySHA256 checks that the file at path has the expected SHA256 hash.
func VerifySHA256(path, expectedHex string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	gotHex := hex.EncodeToString(got[:])
	if gotHex != expectedHex {
		return fmt.Errorf("geoip: sha256 mismatch: got %s, want %s", gotHex, expectedHex)
	}
	return nil
}

// LastUpdated returns the modification time of the database file.
func (s *Service) LastUpdated() time.Time {
	dbPath := filepath.Join(s.cacheDir, s.dbFilename)
	info, err := os.Stat(dbPath)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// parseSHA256Sum extracts the hex hash from a "<hash>  <filename>" formatted string.
func parseSHA256Sum(s string) string {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) >= 1 && len(parts[0]) == 64 {
		return strings.ToLower(parts[0])
	}
	return ""
}
