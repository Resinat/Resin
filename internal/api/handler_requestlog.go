package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/resin-proxy/resin/internal/requestlog"
)

// HandleListRequestLogs handles GET /api/v1/request-logs.
// Query params per DESIGN: from, to (RFC3339Nano), limit, offset,
// platform_id, account, target_host, egress_ip, proxy_type, net_ok, http_status.
func HandleListRequestLogs(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := requestlog.ListFilter{
			PlatformID: q.Get("platform_id"),
			Account:    q.Get("account"),
			TargetHost: q.Get("target_host"),
			EgressIP:   q.Get("egress_ip"),
		}

		// from/to: RFC3339Nano → unix nanoseconds.
		if v := q.Get("from"); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'from': expected RFC3339Nano")
				return
			}
			f.After = t.UnixNano()
		}
		if v := q.Get("to"); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'to': expected RFC3339Nano")
				return
			}
			f.Before = t.UnixNano()
		}

		// from must be < to when both are provided.
		if f.After > 0 && f.Before > 0 && f.After >= f.Before {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "'from' must be before 'to'")
			return
		}

		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 10000 {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'limit': expected integer in [1,10000]")
				return
			}
			f.Limit = n
		}
		if v := q.Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'offset': expected non-negative integer")
				return
			}
			f.Offset = n
		}
		if v := q.Get("proxy_type"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || (n != 1 && n != 2) {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'proxy_type': expected 1 or 2")
				return
			}
			f.ProxyType = &n
		}
		if v := q.Get("net_ok"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || (n != 0 && n != 1) {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'net_ok': expected 0 or 1")
				return
			}
			f.NetOK = &n
		}
		if v := q.Get("http_status"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 100 || n > 599 {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid 'http_status': expected integer in [100,599]")
				return
			}
			f.HTTPStatus = &n
		}

		rows, err := repo.List(f)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		items := make([]logListItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, toLogListItem(row))
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"items": items,
		})
	})
}

// HandleGetRequestLog handles GET /api/v1/request-logs/{log_id}.
func HandleGetRequestLog(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logID := r.PathValue("log_id")
		if logID == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "missing log_id")
			return
		}

		row, err := repo.GetByID(logID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		if row == nil {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}

		writeJSON(w, http.StatusOK, toLogListItem(*row))
	})
}

// HandleGetRequestLogPayloads handles GET /api/v1/request-logs/{log_id}/payloads.
// Returns base64-encoded payloads with truncation metadata per DESIGN spec.
func HandleGetRequestLogPayloads(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logID := r.PathValue("log_id")
		if logID == "" {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "missing log_id")
			return
		}

		// First check the log exists and get truncation info.
		logRow, err := repo.GetByID(logID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		if logRow == nil {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}

		payload, err := repo.GetPayloads(logID)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		resp := payloadResponse{
			Truncated: truncatedInfo{
				ReqHeaders:  logRow.ReqHeadersTruncated,
				ReqBody:     logRow.ReqBodyTruncated,
				RespHeaders: logRow.RespHeadersTruncated,
				RespBody:    logRow.RespBodyTruncated,
			},
		}
		if payload != nil {
			resp.ReqHeadersB64 = base64.StdEncoding.EncodeToString(payload.ReqHeaders)
			resp.ReqBodyB64 = base64.StdEncoding.EncodeToString(payload.ReqBody)
			resp.RespHeadersB64 = base64.StdEncoding.EncodeToString(payload.RespHeaders)
			resp.RespBodyB64 = base64.StdEncoding.EncodeToString(payload.RespBody)
		}

		writeJSON(w, http.StatusOK, resp)
	})
}

// --- Response types ---

type logListItem struct {
	ID                   string `json:"id"`
	Ts                   string `json:"ts"`
	ProxyType            int    `json:"proxy_type"`
	ClientIP             string `json:"client_ip"`
	PlatformID           string `json:"platform_id"`
	PlatformName         string `json:"platform_name"`
	Account              string `json:"account"`
	TargetHost           string `json:"target_host"`
	TargetURL            string `json:"target_url"`
	NodeHash             string `json:"node_hash"`
	NodeTag              string `json:"node_tag"`
	EgressIP             string `json:"egress_ip"`
	DurationMs           int64  `json:"duration_ms"`
	NetOK                int    `json:"net_ok"`
	HTTPMethod           string `json:"http_method"`
	HTTPStatus           int    `json:"http_status"`
	PayloadPresent       int    `json:"payload_present"`
	ReqHeadersLen        int    `json:"req_headers_len"`
	ReqBodyLen           int    `json:"req_body_len"`
	RespHeadersLen       int    `json:"resp_headers_len"`
	RespBodyLen          int    `json:"resp_body_len"`
	ReqHeadersTruncated  bool   `json:"req_headers_truncated"`
	ReqBodyTruncated     bool   `json:"req_body_truncated"`
	RespHeadersTruncated bool   `json:"resp_headers_truncated"`
	RespBodyTruncated    bool   `json:"resp_body_truncated"`
}

func toLogListItem(s requestlog.LogSummary) logListItem {
	netOK := 0
	if s.NetOK {
		netOK = 1
	}
	payloadPresent := 0
	if s.PayloadPresent {
		payloadPresent = 1
	}
	return logListItem{
		ID:                   s.ID,
		Ts:                   time.Unix(0, s.TsNs).UTC().Format(time.RFC3339Nano),
		ProxyType:            s.ProxyType,
		ClientIP:             s.ClientIP,
		PlatformID:           s.PlatformID,
		PlatformName:         s.PlatformName,
		Account:              s.Account,
		TargetHost:           s.TargetHost,
		TargetURL:            s.TargetURL,
		NodeHash:             s.NodeHash,
		NodeTag:              s.NodeTag,
		EgressIP:             s.EgressIP,
		DurationMs:           s.DurationNs / 1e6,
		NetOK:                netOK,
		HTTPMethod:           s.HTTPMethod,
		HTTPStatus:           s.HTTPStatus,
		PayloadPresent:       payloadPresent,
		ReqHeadersLen:        s.ReqHeadersLen,
		ReqBodyLen:           s.ReqBodyLen,
		RespHeadersLen:       s.RespHeadersLen,
		RespBodyLen:          s.RespBodyLen,
		ReqHeadersTruncated:  s.ReqHeadersTruncated,
		ReqBodyTruncated:     s.ReqBodyTruncated,
		RespHeadersTruncated: s.RespHeadersTruncated,
		RespBodyTruncated:    s.RespBodyTruncated,
	}
}

type payloadResponse struct {
	ReqHeadersB64  string        `json:"req_headers_b64"`
	ReqBodyB64     string        `json:"req_body_b64"`
	RespHeadersB64 string        `json:"resp_headers_b64"`
	RespBodyB64    string        `json:"resp_body_b64"`
	Truncated      truncatedInfo `json:"truncated"`
}

type truncatedInfo struct {
	ReqHeaders  bool `json:"req_headers"`
	ReqBody     bool `json:"req_body"`
	RespHeaders bool `json:"resp_headers"`
	RespBody    bool `json:"resp_body"`
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}
