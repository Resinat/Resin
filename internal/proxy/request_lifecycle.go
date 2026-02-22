package proxy

import (
	"net"
	"net/http"
	"time"

	"github.com/Resinat/Resin/internal/routing"
)

// requestLifecycle captures mutable per-request telemetry and emits both
// metrics and request-log events on completion.
type requestLifecycle struct {
	startedAt time.Time
	events    EventEmitter
	finished  RequestFinishedEvent
	log       RequestLogEntry

	reqBodyCapture  *payloadCaptureReadCloser
	respBodyCapture *payloadCaptureReadCloser
}

func newRequestLifecycle(
	events EventEmitter,
	r *http.Request,
	proxyType ProxyType,
	isConnect bool,
) *requestLifecycle {
	method := ""
	clientIP := ""
	if r != nil {
		method = r.Method
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			clientIP = host
		} else {
			clientIP = r.RemoteAddr // fallback: bare IP or unparseable
		}
	}
	now := time.Now()
	return &requestLifecycle{
		startedAt: now,
		events:    events,
		finished: RequestFinishedEvent{
			ProxyType: proxyType,
			IsConnect: isConnect,
		},
		log: RequestLogEntry{
			StartedAtNs: now.UnixNano(),
			ProxyType:   proxyType,
			ClientIP:    clientIP,
			HTTPMethod:  method,
		},
	}
}

func (l *requestLifecycle) finish() {
	if l.reqBodyCapture != nil {
		l.log.ReqBody = l.reqBodyCapture.Payload()
		l.log.ReqBodyLen = l.reqBodyCapture.TotalLen()
		l.log.ReqBodyTruncated = l.reqBodyCapture.Truncated()
	}
	if l.respBodyCapture != nil {
		l.log.RespBody = l.respBodyCapture.Payload()
		l.log.RespBodyLen = l.respBodyCapture.TotalLen()
		l.log.RespBodyTruncated = l.respBodyCapture.Truncated()
	}

	durationNs := time.Since(l.startedAt).Nanoseconds()
	l.finished.DurationNs = durationNs
	l.log.DurationNs = durationNs
	l.events.EmitRequestFinished(l.finished)
	l.events.EmitRequestLog(l.log)
}

func (l *requestLifecycle) setHTTPStatus(code int) {
	l.log.HTTPStatus = code
}

func (l *requestLifecycle) addIngressBytes(n int64) {
	if n > 0 {
		l.log.IngressBytes += n
	}
}

func (l *requestLifecycle) addEgressBytes(n int64) {
	if n > 0 {
		l.log.EgressBytes += n
	}
}

func (l *requestLifecycle) setNetOK(ok bool) {
	l.finished.NetOK = ok
	l.log.NetOK = ok
}

func (l *requestLifecycle) setAccount(account string) {
	l.log.Account = account
}

func (l *requestLifecycle) setTarget(host, rawURL string) {
	l.log.TargetHost = host
	l.log.TargetURL = rawURL
}

func (l *requestLifecycle) setReqHeadersCaptured(reqHeaders []byte, totalLen int, truncated bool) {
	l.log.ReqHeaders = reqHeaders
	l.log.ReqHeadersLen = totalLen
	l.log.ReqHeadersTruncated = truncated
}

func (l *requestLifecycle) setReqBodyCapture(c *payloadCaptureReadCloser) {
	l.reqBodyCapture = c
}

func (l *requestLifecycle) setRespHeadersCaptured(respHeaders []byte, totalLen int, truncated bool) {
	l.log.RespHeaders = respHeaders
	l.log.RespHeadersLen = totalLen
	l.log.RespHeadersTruncated = truncated
}

func (l *requestLifecycle) setRespBodyCapture(c *payloadCaptureReadCloser) {
	l.respBodyCapture = c
}

func (l *requestLifecycle) setRouteResult(result routing.RouteResult) {
	l.finished.PlatformID = result.PlatformID
	l.log.PlatformID = result.PlatformID
	l.log.PlatformName = result.PlatformName
	l.log.NodeHash = result.NodeHash.Hex()
	l.log.NodeTag = result.NodeTag
	l.log.EgressIP = result.EgressIP.String()
}
