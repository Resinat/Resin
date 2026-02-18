package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/resin-proxy/resin/internal/model"
	"github.com/resin-proxy/resin/internal/proxy"
	"github.com/resin-proxy/resin/internal/state"
	"golang.org/x/net/http/httpguts"
)

// ------------------------------------------------------------------
// Account Header Rules
// ------------------------------------------------------------------

// RuleResponse is the API response for an account header rule.
type RuleResponse struct {
	URLPrefix string   `json:"url_prefix"`
	Headers   []string `json:"headers"`
	UpdatedAt string   `json:"updated_at"`
}

func normalizeRulePrefix(prefix string) (string, *ServiceError) {
	normalized, err := proxy.NormalizeRulePrefix(prefix)
	if err != nil {
		return "", invalidArg(err.Error())
	}
	return normalized, nil
}

func ruleToResponse(r model.AccountHeaderRule) (RuleResponse, error) {
	var headers []string
	if err := json.Unmarshal([]byte(r.HeadersJSON), &headers); err != nil {
		return RuleResponse{}, fmt.Errorf("decode headers_json: %w", err)
	}
	for i, h := range headers {
		if strings.TrimSpace(h) == "" {
			return RuleResponse{}, fmt.Errorf("invalid headers_json[%d]: empty header name", i)
		}
		if !httpguts.ValidHeaderFieldName(h) {
			return RuleResponse{}, fmt.Errorf("invalid headers_json[%d] %q", i, h)
		}
	}
	if headers == nil {
		headers = []string{}
	}
	return RuleResponse{
		URLPrefix: r.URLPrefix,
		Headers:   headers,
		UpdatedAt: time.Unix(0, r.UpdatedAtNs).UTC().Format(time.RFC3339Nano),
	}, nil
}

// ListAccountHeaderRules returns all rules.
func (s *ControlPlaneService) ListAccountHeaderRules() ([]RuleResponse, error) {
	rules, err := s.Engine.ListAccountHeaderRules()
	if err != nil {
		return nil, internal("list rules", err)
	}
	resp := make([]RuleResponse, len(rules))
	for i, r := range rules {
		ruleResp, err := ruleToResponse(r)
		if err != nil {
			return nil, internal(fmt.Sprintf("decode rule %q", r.URLPrefix), err)
		}
		resp[i] = ruleResp
	}
	return resp, nil
}

// UpsertAccountHeaderRule creates or updates a rule. Returns (response, created, error).
func (s *ControlPlaneService) UpsertAccountHeaderRule(prefix string, headers []string) (*RuleResponse, bool, error) {
	normalizedPrefix, verr := normalizeRulePrefix(prefix)
	if verr != nil {
		return nil, false, verr
	}
	if len(headers) == 0 {
		return nil, false, invalidArg("headers: must be a non-empty array")
	}
	// Validate header names (RFC 7230 token chars).
	for i, h := range headers {
		if strings.TrimSpace(h) == "" {
			return nil, false, invalidArg(fmt.Sprintf("headers[%d]: must be non-empty", i))
		}
		if !httpguts.ValidHeaderFieldName(h) {
			return nil, false, invalidArg(fmt.Sprintf("headers[%d]: %q is not a valid HTTP header name (RFC 7230 token)", i, h))
		}
	}

	headersJSON, _ := json.Marshal(headers)
	now := time.Now().UnixNano()

	rule := model.AccountHeaderRule{
		URLPrefix:   normalizedPrefix,
		HeadersJSON: string(headersJSON),
		UpdatedAtNs: now,
	}
	created, err := s.Engine.UpsertAccountHeaderRuleWithCreated(rule)
	if err != nil {
		return nil, false, internal("persist rule", err)
	}

	// Hot-update matcher runtime.
	s.reloadAccountMatcher()

	r, err := ruleToResponse(rule)
	if err != nil {
		return nil, false, internal(fmt.Sprintf("decode rule %q", rule.URLPrefix), err)
	}
	return &r, created, nil
}

// DeleteAccountHeaderRule deletes a rule.
func (s *ControlPlaneService) DeleteAccountHeaderRule(prefix string) error {
	normalizedPrefix, verr := normalizeRulePrefix(prefix)
	if verr != nil {
		return verr
	}
	if err := s.Engine.DeleteAccountHeaderRule(normalizedPrefix); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("rule not found")
		}
		return internal("delete rule", err)
	}
	s.reloadAccountMatcher()
	return nil
}

// reloadAccountMatcher reloads all rules and atomically swaps the runtime matcher.
func (s *ControlPlaneService) reloadAccountMatcher() {
	if s.MatcherRuntime == nil {
		return
	}
	rules, err := s.Engine.ListAccountHeaderRules()
	if err != nil {
		log.Printf("[service] reload account matcher failed: %v", err)
		return // best-effort
	}
	s.MatcherRuntime.ReplaceRules(rules)
}

// ResolveResult is the API response for rule resolution.
type ResolveResult struct {
	MatchedURLPrefix string   `json:"matched_url_prefix"`
	Headers          []string `json:"headers"`
}

// ResolveAccountHeaderRule resolves a URL against account header rules.
func (s *ControlPlaneService) ResolveAccountHeaderRule(rawURL string) (*ResolveResult, error) {
	if rawURL == "" {
		return nil, invalidArg("url is required")
	}
	u, err := url.ParseRequestURI(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, invalidArg("url: must be an http/https absolute URL")
	}

	if s.MatcherRuntime == nil {
		return nil, internal("matcher not configured", nil)
	}
	matchedPrefix, headers := s.MatcherRuntime.MatchWithPrefix(u.Host, u.EscapedPath())
	if headers == nil {
		return &ResolveResult{}, nil
	}

	return &ResolveResult{
		MatchedURLPrefix: matchedPrefix,
		Headers:          headers,
	}, nil
}
