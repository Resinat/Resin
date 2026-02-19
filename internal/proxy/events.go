package proxy

type ProxyType int

const (
	ProxyTypeForward ProxyType = 1
	ProxyTypeReverse ProxyType = 2
)

// ConnectionDirection indicates inbound vs outbound connection flow.
type ConnectionDirection int

const (
	ConnectionInbound ConnectionDirection = iota
	ConnectionOutbound
)

// ConnectionOp is the operation type for a connection lifecycle event.
type ConnectionOp int

const (
	ConnectionOpen ConnectionOp = iota
	ConnectionClose
)

// ConnectionLifecycleEvent tracks connection open/close.
type ConnectionLifecycleEvent struct {
	Direction ConnectionDirection
	Op        ConnectionOp
}

// RequestFinishedEvent is emitted when a proxy request completes.
// Used by the metrics subsystem (Phase 8).
type RequestFinishedEvent struct {
	PlatformID string
	ProxyType  ProxyType // 1=forward, 2=reverse
	IsConnect  bool
	NetOK      bool
	DurationNs int64
}

// RequestLogEntry captures per-request details for the structured request log.
// Used by the requestlog subsystem (Phase 8).
type RequestLogEntry struct {
	ID           string    // optional stable ID; repo generates one when empty
	StartedAtNs  int64     // request start time (Unix nano), used as ts_ns in DB
	ProxyType    ProxyType // 1=forward, 2=reverse
	ClientIP     string
	PlatformID   string
	PlatformName string
	Account      string
	TargetHost   string
	TargetURL    string
	NodeHash     string
	NodeTag      string // display tag: "<Subscription>/<Tag>" (DESIGN.md §601)
	EgressIP     string
	DurationNs   int64
	NetOK        bool
	HTTPMethod   string
	HTTPStatus   int

	// Optional detail payload (mainly for reverse proxy request logging).
	ReqHeaders           []byte
	ReqHeadersLen        int
	ReqHeadersTruncated  bool
	ReqBody              []byte
	ReqBodyLen           int
	ReqBodyTruncated     bool
	RespHeaders          []byte
	RespHeadersLen       int
	RespHeadersTruncated bool
	RespBody             []byte
	RespBodyLen          int
	RespBodyTruncated    bool
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
func (NoOpEventEmitter) EmitRequestLog(RequestLogEntry)           {}

// ConfigAwareEventEmitter wraps another EventEmitter and gates request-log
// emission by a runtime flag provider (hot-reload friendly).
type ConfigAwareEventEmitter struct {
	Base              EventEmitter
	RequestLogEnabled func() bool

	// Reverse proxy request-log detail controls (hot-reload friendly).
	ReverseProxyLogDetailEnabled       func() bool
	ReverseProxyLogReqHeadersMaxBytes  func() int
	ReverseProxyLogReqBodyMaxBytes     func() int
	ReverseProxyLogRespHeadersMaxBytes func() int
	ReverseProxyLogRespBodyMaxBytes    func() int
}

type reverseDetailCaptureConfig struct {
	Enabled             bool
	ReqHeadersMaxBytes  int
	ReqBodyMaxBytes     int
	RespHeadersMaxBytes int
	RespBodyMaxBytes    int
}

func (e ConfigAwareEventEmitter) emitBase() EventEmitter {
	if e.Base == nil {
		return NoOpEventEmitter{}
	}
	return e.Base
}

func (e ConfigAwareEventEmitter) reverseDetailCaptureConfig() reverseDetailCaptureConfig {
	cfg := reverseDetailCaptureConfig{
		Enabled:             true,
		ReqHeadersMaxBytes:  -1,
		ReqBodyMaxBytes:     -1,
		RespHeadersMaxBytes: -1,
		RespBodyMaxBytes:    -1,
	}
	if e.RequestLogEnabled != nil && !e.RequestLogEnabled() {
		cfg.Enabled = false
	}
	if e.ReverseProxyLogDetailEnabled != nil && !e.ReverseProxyLogDetailEnabled() {
		cfg.Enabled = false
	}
	if e.ReverseProxyLogReqHeadersMaxBytes != nil {
		cfg.ReqHeadersMaxBytes = e.ReverseProxyLogReqHeadersMaxBytes()
	}
	if e.ReverseProxyLogReqBodyMaxBytes != nil {
		cfg.ReqBodyMaxBytes = e.ReverseProxyLogReqBodyMaxBytes()
	}
	if e.ReverseProxyLogRespHeadersMaxBytes != nil {
		cfg.RespHeadersMaxBytes = e.ReverseProxyLogRespHeadersMaxBytes()
	}
	if e.ReverseProxyLogRespBodyMaxBytes != nil {
		cfg.RespBodyMaxBytes = e.ReverseProxyLogRespBodyMaxBytes()
	}
	return cfg
}

func normalizePayloadField(payload []byte, length int, truncated bool, max int) ([]byte, int, bool) {
	if length < len(payload) {
		length = len(payload)
	}
	if max >= 0 && len(payload) > max {
		payload = payload[:max]
		truncated = true
	}
	if len(payload) == 0 {
		payload = nil
	} else {
		payload = append([]byte(nil), payload...)
	}
	return payload, length, truncated
}

func clearReverseDetailPayload(ev *RequestLogEntry) {
	ev.ReqHeaders = nil
	ev.ReqHeadersLen = 0
	ev.ReqHeadersTruncated = false
	ev.ReqBody = nil
	ev.ReqBodyLen = 0
	ev.ReqBodyTruncated = false
	ev.RespHeaders = nil
	ev.RespHeadersLen = 0
	ev.RespHeadersTruncated = false
	ev.RespBody = nil
	ev.RespBodyLen = 0
	ev.RespBodyTruncated = false
}

func (e ConfigAwareEventEmitter) EmitRequestFinished(ev RequestFinishedEvent) {
	e.emitBase().EmitRequestFinished(ev)
}

func (e ConfigAwareEventEmitter) EmitRequestLog(ev RequestLogEntry) {
	if e.RequestLogEnabled != nil && !e.RequestLogEnabled() {
		return
	}

	// Reverse proxy detail payload is controlled by runtime config.
	if ev.ProxyType == ProxyTypeReverse {
		cfg := e.reverseDetailCaptureConfig()
		if !cfg.Enabled {
			clearReverseDetailPayload(&ev)
		} else {
			ev.ReqHeaders, ev.ReqHeadersLen, ev.ReqHeadersTruncated = normalizePayloadField(
				ev.ReqHeaders,
				ev.ReqHeadersLen,
				ev.ReqHeadersTruncated,
				cfg.ReqHeadersMaxBytes,
			)
			ev.ReqBody, ev.ReqBodyLen, ev.ReqBodyTruncated = normalizePayloadField(
				ev.ReqBody,
				ev.ReqBodyLen,
				ev.ReqBodyTruncated,
				cfg.ReqBodyMaxBytes,
			)
			ev.RespHeaders, ev.RespHeadersLen, ev.RespHeadersTruncated = normalizePayloadField(
				ev.RespHeaders,
				ev.RespHeadersLen,
				ev.RespHeadersTruncated,
				cfg.RespHeadersMaxBytes,
			)
			ev.RespBody, ev.RespBodyLen, ev.RespBodyTruncated = normalizePayloadField(
				ev.RespBody,
				ev.RespBodyLen,
				ev.RespBodyTruncated,
				cfg.RespBodyMaxBytes,
			)
		}
	}

	e.emitBase().EmitRequestLog(ev)
}
