package proxy

// RequestFinishedEvent is emitted when a proxy request completes.
// Used by the metrics subsystem (Phase 8).
type RequestFinishedEvent struct {
	PlatformID string
	ProxyType  string // "forward" | "reverse"
	IsConnect  bool
	NetOK      bool
	DurationNs int64
}

// RequestLogEntry captures per-request details for the structured request log.
// Used by the requestlog subsystem (Phase 7).
type RequestLogEntry struct {
	ProxyType    int    // 1=forward, 2=reverse
	ClientIP     string
	PlatformID   string
	PlatformName string
	Account      string
	TargetHost   string
	TargetURL    string
	NodeHash     string
	EgressIP     string
	DurationNs   int64
	NetOK        bool
	HTTPMethod   string
	HTTPStatus   int
}

// EventEmitter defines the interface for proxy-layer event emission.
// Covers both metrics and requestlog event paths (STAGES.md Task 8).
type EventEmitter interface {
	EmitRequestFinished(RequestFinishedEvent)
	EmitRequestLog(RequestLogEntry)
}

// NoOpEventEmitter is a no-op implementation used until Phase 7/8.
type NoOpEventEmitter struct{}

func (NoOpEventEmitter) EmitRequestFinished(RequestFinishedEvent) {}
func (NoOpEventEmitter) EmitRequestLog(RequestLogEntry)          {}
