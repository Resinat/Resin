package proxy

import "testing"

type recordingEmitter struct {
	finished int
	logs     int
	lastLog  RequestLogEntry
}

func (e *recordingEmitter) EmitRequestFinished(RequestFinishedEvent) {
	e.finished++
}

func (e *recordingEmitter) EmitRequestLog(entry RequestLogEntry) {
	e.logs++
	e.lastLog = entry
}

func TestConfigAwareEventEmitter_DisabledRequestLog(t *testing.T) {
	base := &recordingEmitter{}
	emitter := ConfigAwareEventEmitter{
		Base:              base,
		RequestLogEnabled: func() bool { return false },
	}

	emitter.EmitRequestFinished(RequestFinishedEvent{})
	emitter.EmitRequestLog(RequestLogEntry{})

	if base.finished != 1 {
		t.Fatalf("finished = %d, want 1", base.finished)
	}
	if base.logs != 0 {
		t.Fatalf("logs = %d, want 0", base.logs)
	}
}

func TestConfigAwareEventEmitter_EnabledRequestLog(t *testing.T) {
	base := &recordingEmitter{}
	emitter := ConfigAwareEventEmitter{
		Base:              base,
		RequestLogEnabled: func() bool { return true },
	}

	emitter.EmitRequestLog(RequestLogEntry{})
	if base.logs != 1 {
		t.Fatalf("logs = %d, want 1", base.logs)
	}
}

func TestConfigAwareEventEmitter_NilBase(t *testing.T) {
	emitter := ConfigAwareEventEmitter{
		RequestLogEnabled: func() bool { return true },
	}
	emitter.EmitRequestFinished(RequestFinishedEvent{})
	emitter.EmitRequestLog(RequestLogEntry{})
}

func TestConfigAwareEventEmitter_ReverseHeadersTruncationHotReload(t *testing.T) {
	base := &recordingEmitter{}

	reqHeadersMaxBytes := 4
	reqBodyMaxBytes := 3
	respHeadersMaxBytes := 5
	respBodyMaxBytes := 2
	detailEnabled := true

	emitter := ConfigAwareEventEmitter{
		Base:                         base,
		RequestLogEnabled:            func() bool { return true },
		ReverseProxyLogDetailEnabled: func() bool { return detailEnabled },
		ReverseProxyLogReqHeadersMaxBytes: func() int {
			return reqHeadersMaxBytes
		},
		ReverseProxyLogReqBodyMaxBytes: func() int {
			return reqBodyMaxBytes
		},
		ReverseProxyLogRespHeadersMaxBytes: func() int {
			return respHeadersMaxBytes
		},
		ReverseProxyLogRespBodyMaxBytes: func() int {
			return respBodyMaxBytes
		},
	}

	input := RequestLogEntry{
		ProxyType:   ProxyTypeReverse,
		ReqHeaders:  []byte("0123456789"),
		ReqBody:     []byte("abcdef"),
		RespHeaders: []byte("zyxwvutsrq"),
		RespBody:    []byte("ok!"),
	}

	emitter.EmitRequestLog(input)
	if base.lastLog.ReqHeadersLen != 10 {
		t.Fatalf("ReqHeadersLen = %d, want 10", base.lastLog.ReqHeadersLen)
	}
	if !base.lastLog.ReqHeadersTruncated {
		t.Fatal("ReqHeadersTruncated = false, want true")
	}
	if string(base.lastLog.ReqHeaders) != "0123" {
		t.Fatalf("ReqHeaders = %q, want %q", string(base.lastLog.ReqHeaders), "0123")
	}
	if base.lastLog.ReqBodyLen != 6 {
		t.Fatalf("ReqBodyLen = %d, want 6", base.lastLog.ReqBodyLen)
	}
	if !base.lastLog.ReqBodyTruncated {
		t.Fatal("ReqBodyTruncated = false, want true")
	}
	if string(base.lastLog.ReqBody) != "abc" {
		t.Fatalf("ReqBody = %q, want %q", string(base.lastLog.ReqBody), "abc")
	}
	if base.lastLog.RespHeadersLen != 10 {
		t.Fatalf("RespHeadersLen = %d, want 10", base.lastLog.RespHeadersLen)
	}
	if !base.lastLog.RespHeadersTruncated {
		t.Fatal("RespHeadersTruncated = false, want true")
	}
	if string(base.lastLog.RespHeaders) != "zyxwv" {
		t.Fatalf("RespHeaders = %q, want %q", string(base.lastLog.RespHeaders), "zyxwv")
	}
	if base.lastLog.RespBodyLen != 3 {
		t.Fatalf("RespBodyLen = %d, want 3", base.lastLog.RespBodyLen)
	}
	if !base.lastLog.RespBodyTruncated {
		t.Fatal("RespBodyTruncated = false, want true")
	}
	if string(base.lastLog.RespBody) != "ok" {
		t.Fatalf("RespBody = %q, want %q", string(base.lastLog.RespBody), "ok")
	}

	// Hot-reload simulation: increase runtime threshold without recreating emitter.
	reqHeadersMaxBytes = 12
	reqBodyMaxBytes = 12
	respHeadersMaxBytes = 12
	respBodyMaxBytes = 12
	emitter.EmitRequestLog(input)
	if base.lastLog.ReqHeadersLen != 10 {
		t.Fatalf("ReqHeadersLen(after reload) = %d, want 10", base.lastLog.ReqHeadersLen)
	}
	if base.lastLog.ReqHeadersTruncated {
		t.Fatal("ReqHeadersTruncated(after reload) = true, want false")
	}
	if string(base.lastLog.ReqHeaders) != "0123456789" {
		t.Fatalf("ReqHeaders(after reload) = %q, want %q", string(base.lastLog.ReqHeaders), "0123456789")
	}
	if base.lastLog.ReqBodyTruncated {
		t.Fatal("ReqBodyTruncated(after reload) = true, want false")
	}
	if string(base.lastLog.ReqBody) != "abcdef" {
		t.Fatalf("ReqBody(after reload) = %q, want %q", string(base.lastLog.ReqBody), "abcdef")
	}
	if base.lastLog.RespHeadersTruncated {
		t.Fatal("RespHeadersTruncated(after reload) = true, want false")
	}
	if string(base.lastLog.RespHeaders) != "zyxwvutsrq" {
		t.Fatalf("RespHeaders(after reload) = %q, want %q", string(base.lastLog.RespHeaders), "zyxwvutsrq")
	}
	if base.lastLog.RespBodyTruncated {
		t.Fatal("RespBodyTruncated(after reload) = true, want false")
	}
	if string(base.lastLog.RespBody) != "ok!" {
		t.Fatalf("RespBody(after reload) = %q, want %q", string(base.lastLog.RespBody), "ok!")
	}

	// Disable detail logging at runtime.
	detailEnabled = false
	emitter.EmitRequestLog(input)
	if base.lastLog.ReqHeadersLen != 0 {
		t.Fatalf("ReqHeadersLen(detail off) = %d, want 0", base.lastLog.ReqHeadersLen)
	}
	if base.lastLog.ReqHeadersTruncated {
		t.Fatal("ReqHeadersTruncated(detail off) = true, want false")
	}
	if len(base.lastLog.ReqHeaders) != 0 {
		t.Fatalf("ReqHeaders(detail off) len = %d, want 0", len(base.lastLog.ReqHeaders))
	}
	if base.lastLog.ReqBodyLen != 0 || len(base.lastLog.ReqBody) != 0 || base.lastLog.ReqBodyTruncated {
		t.Fatalf("ReqBody(detail off) = len:%d payload:%d truncated:%v, want 0,0,false",
			base.lastLog.ReqBodyLen, len(base.lastLog.ReqBody), base.lastLog.ReqBodyTruncated)
	}
	if base.lastLog.RespHeadersLen != 0 || len(base.lastLog.RespHeaders) != 0 || base.lastLog.RespHeadersTruncated {
		t.Fatalf("RespHeaders(detail off) = len:%d payload:%d truncated:%v, want 0,0,false",
			base.lastLog.RespHeadersLen, len(base.lastLog.RespHeaders), base.lastLog.RespHeadersTruncated)
	}
	if base.lastLog.RespBodyLen != 0 || len(base.lastLog.RespBody) != 0 || base.lastLog.RespBodyTruncated {
		t.Fatalf("RespBody(detail off) = len:%d payload:%d truncated:%v, want 0,0,false",
			base.lastLog.RespBodyLen, len(base.lastLog.RespBody), base.lastLog.RespBodyTruncated)
	}
}
