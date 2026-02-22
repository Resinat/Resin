package api

import (
	"net/http"
	"strings"

	"github.com/Resinat/Resin/internal/service"
)

func ruleMatchesKeyword(rule service.RuleResponse, keyword string) bool {
	contains := func(v string) bool {
		return strings.Contains(strings.ToLower(v), keyword)
	}

	if contains(rule.URLPrefix) {
		return true
	}
	for _, header := range rule.Headers {
		if contains(header) {
			return true
		}
	}
	return false
}

func filterRulesByKeyword(rules []service.RuleResponse, rawKeyword string) []service.RuleResponse {
	keyword := strings.ToLower(strings.TrimSpace(rawKeyword))
	if keyword == "" {
		return rules
	}
	filtered := make([]service.RuleResponse, 0, len(rules))
	for _, rule := range rules {
		if ruleMatchesKeyword(rule, keyword) {
			filtered = append(filtered, rule)
		}
	}
	return filtered
}

// HandleListRules returns a handler for GET /api/v1/account-header-rules.
func HandleListRules(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := cp.ListAccountHeaderRules()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		rules = filterRulesByKeyword(rules, r.URL.Query().Get("keyword"))
		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, rules, pg)
	}
}

// HandleUpsertRule returns a handler for:
//   - PUT /api/v1/account-header-rules/{prefix...}
func HandleUpsertRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URLPrefix string   `json:"url_prefix"`
			Headers   []string `json:"headers"`
		}
		if err := DecodeBody(r, &body); err != nil {
			writeDecodeBodyError(w, err)
			return
		}

		prefix := PathParam(r, "prefix")
		if prefix == "" {
			writeInvalidArgument(w, "url_prefix must be provided in path")
			return
		}
		if body.URLPrefix != "" {
			writeInvalidArgument(w, "url_prefix must be provided in path, not body")
			return
		}

		rule, created, err := cp.UpsertAccountHeaderRule(prefix, body.Headers)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		WriteJSON(w, status, rule)
	}
}

// HandleDeleteRule returns a handler for DELETE /api/v1/account-header-rules/{prefix}.
func HandleDeleteRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		prefix := PathParam(r, "prefix")
		if err := cp.DeleteAccountHeaderRule(prefix); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleResolveRule returns a handler for POST /api/v1/account-header-rules:resolve.
func HandleResolveRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if err := DecodeBody(r, &body); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		result, err := cp.ResolveAccountHeaderRule(body.URL)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}
