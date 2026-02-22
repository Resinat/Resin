package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/requestlog"
)

// HandleListRequestLogs handles GET /api/v1/request-logs.
// Query params: from, to (RFC3339Nano), limit, cursor,
// platform_id, platform_name, account, target_host, egress_ip, proxy_type, net_ok, http_status, fuzzy.
func HandleListRequestLogs(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			writeInvalidArgument(w, "offset: not supported for request-logs; use cursor")
			return
		}

		limit, ok := parseRequestLogLimitQuery(w, r)
		if !ok {
			return
		}

		cursor, ok := parseRequestLogCursorQuery(w, r)
		if !ok {
			return
		}

		q := r.URL.Query()
		f := requestlog.ListFilter{
			PlatformID:   q.Get("platform_id"),
			PlatformName: q.Get("platform_name"),
			Account:      q.Get("account"),
			TargetHost:   q.Get("target_host"),
			EgressIP:     q.Get("egress_ip"),
			Limit:        limit,
			Cursor:       cursor,
		}

		if v := q.Get("from"); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				writeInvalidArgument(w, "from: invalid RFC3339 timestamp")
				return
			}
			f.After = t.UnixNano()
		}
		if v := q.Get("to"); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				writeInvalidArgument(w, "to: invalid RFC3339 timestamp")
				return
			}
			f.Before = t.UnixNano()
		}
		if f.After > 0 && f.Before > 0 && f.After >= f.Before {
			writeInvalidArgument(w, "from: must be before to")
			return
		}

		proxyType, ok := parseBoundedIntQuery(w, r, "proxy_type", 1, 2, "proxy_type: must be 1 or 2")
		if !ok {
			return
		}
		f.ProxyType = proxyType

		netOK, ok := parseStrictBoolQuery(w, r, "net_ok")
		if !ok {
			return
		}
		f.NetOK = netOK

		httpStatus, ok := parseBoundedIntQuery(w, r, "http_status", 100, 599, "http_status: must be in [100,599]")
		if !ok {
			return
		}
		f.HTTPStatus = httpStatus

		fuzzy, ok := parseStrictBoolQuery(w, r, "fuzzy")
		if !ok {
			return
		}
		if fuzzy != nil {
			f.Fuzzy = *fuzzy
		}

		rows, hasMore, nextCursor, err := repo.List(f)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}

		items := make([]logListItem, 0, len(rows))
		for _, row := range rows {
			items = append(items, toLogListItem(row))
		}

		resp := requestLogPageResponse{
			Items:   items,
			Limit:   limit,
			HasMore: hasMore,
		}
		if nextCursor != nil {
			resp.NextCursor = encodeRequestLogCursor(*nextCursor)
		}
		WriteJSON(w, http.StatusOK, resp)
	})
}

func parseRequestLogLimitQuery(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := defaultPageLimit
	v := r.URL.Query().Get("limit")
	if v == "" {
		return limit, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		writeInvalidArgument(w, "limit: must be a non-negative integer")
		return 0, false
	}
	if n > maxPageLimit {
		writeInvalidArgument(w, "limit: must be <= "+strconv.Itoa(maxPageLimit))
		return 0, false
	}
	if n > 0 {
		limit = n
	}
	return limit, true
}

func parseRequestLogCursorQuery(w http.ResponseWriter, r *http.Request) (*requestlog.ListCursor, bool) {
	v := r.URL.Query().Get("cursor")
	if v == "" {
		return nil, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		writeInvalidArgument(w, "cursor: invalid format")
		return nil, false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeInvalidArgument(w, "cursor: invalid format")
		return nil, false
	}
	tsNs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeInvalidArgument(w, "cursor: invalid format")
		return nil, false
	}
	return &requestlog.ListCursor{
		TsNs: tsNs,
		ID:   parts[1],
	}, true
}

func encodeRequestLogCursor(c requestlog.ListCursor) string {
	raw := strconv.FormatInt(c.TsNs, 10) + ":" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

type requestLogPageResponse struct {
	Items      []logListItem `json:"items"`
	Limit      int           `json:"limit"`
	HasMore    bool          `json:"has_more"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// HandleGetRequestLog handles GET /api/v1/request-logs/{log_id}.
func HandleGetRequestLog(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logID := r.PathValue("log_id")
		if logID == "" {
			writeInvalidArgument(w, "log_id: is required")
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

		WriteJSON(w, http.StatusOK, toLogListItem(*row))
	})
}

// HandleGetRequestLogPayloads handles GET /api/v1/request-logs/{log_id}/payloads.
// Returns base64-encoded payloads with truncation metadata.
func HandleGetRequestLogPayloads(repo *requestlog.Repo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logID := r.PathValue("log_id")
		if logID == "" {
			writeInvalidArgument(w, "log_id: is required")
			return
		}

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

		WriteJSON(w, http.StatusOK, resp)
	})
}

func parseBoundedIntQuery(
	w http.ResponseWriter,
	r *http.Request,
	key string,
	min int,
	max int,
	errMsg string,
) (*int, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		writeInvalidArgument(w, errMsg)
		return nil, false
	}
	return &n, true
}

func parseStrictBoolQuery(w http.ResponseWriter, r *http.Request, key string) (*bool, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, true
	}
	switch v {
	case "true":
		b := true
		return &b, true
	case "false":
		b := false
		return &b, true
	default:
		writeInvalidArgument(w, key+": must be true or false")
		return nil, false
	}
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
	NetOK                bool   `json:"net_ok"`
	HTTPMethod           string `json:"http_method"`
	HTTPStatus           int    `json:"http_status"`
	IngressBytes         int64  `json:"ingress_bytes"`
	EgressBytes          int64  `json:"egress_bytes"`
	PayloadPresent       bool   `json:"payload_present"`
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
		NetOK:                s.NetOK,
		HTTPMethod:           s.HTTPMethod,
		HTTPStatus:           s.HTTPStatus,
		IngressBytes:         s.IngressBytes,
		EgressBytes:          s.EgressBytes,
		PayloadPresent:       s.PayloadPresent,
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
