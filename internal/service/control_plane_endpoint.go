package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/state"
)

const DefaultEndpointID = "default"

// EndpointRuntimeStatus describes the ephemeral state of one inbound listener.
type EndpointRuntimeStatus struct {
	State     string
	LastError string
}

// EndpointRuntime applies persisted endpoint configuration to network listeners.
type EndpointRuntime interface {
	ApplyEndpoint(model.Endpoint) error
	RemoveEndpoint(id string)
	EndpointStatus(id string) EndpointRuntimeStatus
}

type EndpointResponse struct {
	ID                   string `json:"id"`
	Port                 int    `json:"port"`
	Enabled              bool   `json:"enabled"`
	AllowManagement      bool   `json:"allow_management"`
	AllowProxy           bool   `json:"allow_proxy"`
	RequireProxyAuthInfo bool   `json:"require_proxy_auth_info"`
	AllowHTTPForward     bool   `json:"allow_http_forward"`
	AllowHTTPReverse     bool   `json:"allow_http_reverse"`
	AllowSOCKS5          bool   `json:"allow_socks5"`
	Source               string `json:"source"`
	ReadOnly             bool   `json:"read_only"`
	Status               string `json:"status"`
	LastError            string `json:"last_error,omitempty"`
	CreatedAt            string `json:"created_at,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
}

type CreateEndpointRequest struct {
	Port                 int   `json:"port"`
	Enabled              *bool `json:"enabled,omitempty"`
	AllowManagement      *bool `json:"allow_management,omitempty"`
	AllowProxy           *bool `json:"allow_proxy,omitempty"`
	RequireProxyAuthInfo *bool `json:"require_proxy_auth_info,omitempty"`
	AllowHTTPForward     *bool `json:"allow_http_forward,omitempty"`
	AllowHTTPReverse     *bool `json:"allow_http_reverse,omitempty"`
	AllowSOCKS5          *bool `json:"allow_socks5,omitempty"`
}

var endpointPatchAllowedFields = map[string]bool{
	"enabled":                 true,
	"port":                    true,
	"allow_management":        true,
	"allow_proxy":             true,
	"require_proxy_auth_info": true,
	"allow_http_forward":      true,
	"allow_http_reverse":      true,
	"allow_socks5":            true,
}

func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// NewDefaultEndpoint builds the environment-defined, read-only endpoint policy.
func NewDefaultEndpoint(port int) model.Endpoint {
	if port == 0 {
		port = 2260
	}
	return model.Endpoint{
		ID:               DefaultEndpointID,
		Port:             port,
		Enabled:          true,
		AllowManagement:  true,
		AllowProxy:       true,
		AllowHTTPForward: true,
		AllowHTTPReverse: true,
		AllowSOCKS5:      true,
	}
}

func (s *ControlPlaneService) defaultEndpoint() model.Endpoint {
	port := 0
	if s != nil && s.EnvCfg != nil {
		port = s.EnvCfg.ResinPort
	}
	return NewDefaultEndpoint(port)
}

func (s *ControlPlaneService) endpointResponse(endpoint model.Endpoint, source string, readOnly bool) EndpointResponse {
	status := EndpointRuntimeStatus{State: "inactive"}
	if s != nil && s.EndpointRuntime != nil {
		status = s.EndpointRuntime.EndpointStatus(endpoint.ID)
		if status.State == "" {
			status.State = "inactive"
		}
	}
	response := EndpointResponse{
		ID:                   endpoint.ID,
		Port:                 endpoint.Port,
		Enabled:              endpoint.Enabled,
		AllowManagement:      endpoint.AllowManagement,
		AllowProxy:           endpoint.AllowProxy,
		RequireProxyAuthInfo: endpoint.RequireProxyAuthInfo,
		AllowHTTPForward:     endpoint.AllowHTTPForward,
		AllowHTTPReverse:     endpoint.AllowHTTPReverse,
		AllowSOCKS5:          endpoint.AllowSOCKS5,
		Source:               source,
		ReadOnly:             readOnly,
		Status:               status.State,
		LastError:            status.LastError,
	}
	if endpoint.CreatedAtNs > 0 {
		response.CreatedAt = time.Unix(0, endpoint.CreatedAtNs).UTC().Format(time.RFC3339Nano)
	}
	if endpoint.UpdatedAtNs > 0 {
		response.UpdatedAt = time.Unix(0, endpoint.UpdatedAtNs).UTC().Format(time.RFC3339Nano)
	}
	return response
}

func (s *ControlPlaneService) validateEndpoint(endpoint model.Endpoint) *ServiceError {
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return invalidArg("port: must be between 1 and 65535")
	}
	if endpoint.ID != DefaultEndpointID && endpoint.Port == s.defaultEndpoint().Port {
		return conflict("port is reserved by the default endpoint")
	}
	if !endpoint.AllowManagement && !endpoint.AllowProxy {
		return invalidArg("at least one of allow_management or allow_proxy must be enabled")
	}
	if !endpoint.AllowProxy {
		if endpoint.AllowHTTPForward || endpoint.AllowHTTPReverse || endpoint.AllowSOCKS5 || endpoint.RequireProxyAuthInfo {
			return invalidArg("proxy protocol settings must be disabled when allow_proxy is false")
		}
		return nil
	}
	if !endpoint.AllowHTTPForward && !endpoint.AllowHTTPReverse && !endpoint.AllowSOCKS5 {
		return invalidArg("at least one proxy protocol must be enabled when allow_proxy is true")
	}
	if endpoint.RequireProxyAuthInfo && !endpoint.AllowHTTPForward && !endpoint.AllowSOCKS5 {
		return invalidArg("require_proxy_auth_info requires HTTP forward proxy or SOCKS5")
	}
	return nil
}

func (s *ControlPlaneService) ListEndpoints() ([]EndpointResponse, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("endpoint service is not initialized", nil)
	}
	s.endpointMu.RLock()
	defer s.endpointMu.RUnlock()

	custom, err := s.Engine.ListEndpoints()
	if err != nil {
		return nil, internal("list endpoints", err)
	}
	result := make([]EndpointResponse, 0, len(custom)+1)
	result = append(result, s.endpointResponse(s.defaultEndpoint(), "environment", true))
	for _, endpoint := range custom {
		result = append(result, s.endpointResponse(endpoint, "database", false))
	}
	return result, nil
}

func (s *ControlPlaneService) GetEndpoint(id string) (*EndpointResponse, error) {
	if id == DefaultEndpointID {
		response := s.endpointResponse(s.defaultEndpoint(), "environment", true)
		return &response, nil
	}
	if s == nil || s.Engine == nil {
		return nil, internal("endpoint service is not initialized", nil)
	}
	s.endpointMu.RLock()
	defer s.endpointMu.RUnlock()

	endpoint, err := s.Engine.GetEndpoint(id)
	if errors.Is(err, state.ErrNotFound) {
		return nil, notFound("endpoint not found")
	}
	if err != nil {
		return nil, internal("get endpoint", err)
	}
	response := s.endpointResponse(*endpoint, "database", false)
	return &response, nil
}

func (s *ControlPlaneService) CreateEndpoint(req CreateEndpointRequest) (*EndpointResponse, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("endpoint service is not initialized", nil)
	}
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()

	allowProxy := boolOrDefault(req.AllowProxy, true)
	now := time.Now().UnixNano()
	endpoint := model.Endpoint{
		ID:                   uuid.New().String(),
		Port:                 req.Port,
		Enabled:              boolOrDefault(req.Enabled, true),
		AllowManagement:      boolOrDefault(req.AllowManagement, false),
		AllowProxy:           allowProxy,
		RequireProxyAuthInfo: boolOrDefault(req.RequireProxyAuthInfo, false),
		AllowHTTPForward:     boolOrDefault(req.AllowHTTPForward, allowProxy),
		AllowHTTPReverse:     boolOrDefault(req.AllowHTTPReverse, allowProxy),
		AllowSOCKS5:          boolOrDefault(req.AllowSOCKS5, allowProxy),
		CreatedAtNs:          now,
		UpdatedAtNs:          now,
	}
	if err := s.validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	if err := s.Engine.InsertEndpoint(endpoint); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return nil, conflict("endpoint port already exists")
		}
		return nil, internal("persist endpoint", err)
	}
	if endpoint.Enabled && s.EndpointRuntime != nil {
		if err := s.EndpointRuntime.ApplyEndpoint(endpoint); err != nil {
			s.EndpointRuntime.RemoveEndpoint(endpoint.ID)
			if rollbackErr := s.Engine.DeleteEndpoint(endpoint.ID); rollbackErr != nil {
				return nil, internal("rollback endpoint after listener failure", errors.Join(err, rollbackErr))
			}
			return nil, conflict(fmt.Sprintf("listen on port %d: %v", endpoint.Port, err))
		}
	}
	response := s.endpointResponse(endpoint, "database", false)
	return &response, nil
}

func (s *ControlPlaneService) UpdateEndpoint(id string, patchJSON json.RawMessage) (*EndpointResponse, error) {
	if id == DefaultEndpointID {
		return nil, conflict("default endpoint is read-only")
	}
	if s == nil || s.Engine == nil {
		return nil, internal("endpoint service is not initialized", nil)
	}
	patch, patchErr := parseMergePatch(patchJSON)
	if patchErr != nil {
		return nil, patchErr
	}
	if err := patch.validateFields(endpointPatchAllowedFields, func(key string) string {
		return fmt.Sprintf("field %q is read-only or unknown", key)
	}); err != nil {
		return nil, err
	}

	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()
	current, err := s.Engine.GetEndpoint(id)
	if errors.Is(err, state.ErrNotFound) {
		return nil, notFound("endpoint not found")
	}
	if err != nil {
		return nil, internal("get endpoint", err)
	}
	next := *current
	if value, ok, parseErr := patch.optionalInt("port"); parseErr != nil {
		return nil, parseErr
	} else if ok {
		next.Port = value
	}
	boolFields := []struct {
		name string
		set  func(bool)
	}{
		{"enabled", func(v bool) { next.Enabled = v }},
		{"allow_management", func(v bool) { next.AllowManagement = v }},
		{"allow_proxy", func(v bool) { next.AllowProxy = v }},
		{"require_proxy_auth_info", func(v bool) { next.RequireProxyAuthInfo = v }},
		{"allow_http_forward", func(v bool) { next.AllowHTTPForward = v }},
		{"allow_http_reverse", func(v bool) { next.AllowHTTPReverse = v }},
		{"allow_socks5", func(v bool) { next.AllowSOCKS5 = v }},
	}
	for _, field := range boolFields {
		value, ok, parseErr := patch.optionalBool(field.name)
		if parseErr != nil {
			return nil, parseErr
		}
		if ok {
			field.set(value)
		}
	}
	if validationErr := s.validateEndpoint(next); validationErr != nil {
		return nil, validationErr
	}
	if next == *current {
		response := s.endpointResponse(*current, "database", false)
		return &response, nil
	}
	next.UpdatedAtNs = time.Now().UnixNano()
	if err := s.Engine.UpdateEndpoint(next); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return nil, conflict("endpoint port already exists")
		}
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("endpoint not found")
		}
		return nil, internal("persist endpoint", err)
	}
	if next.Enabled && s.EndpointRuntime != nil {
		if applyErr := s.EndpointRuntime.ApplyEndpoint(next); applyErr != nil {
			if !current.Enabled {
				s.EndpointRuntime.RemoveEndpoint(next.ID)
			}
			if rollbackErr := s.Engine.UpdateEndpoint(*current); rollbackErr != nil {
				return nil, internal("rollback endpoint after listener failure", errors.Join(applyErr, rollbackErr))
			}
			return nil, conflict(fmt.Sprintf("listen on port %d: %v", next.Port, applyErr))
		}
	} else if s.EndpointRuntime != nil {
		s.EndpointRuntime.RemoveEndpoint(next.ID)
	}
	response := s.endpointResponse(next, "database", false)
	return &response, nil
}

func (s *ControlPlaneService) DeleteEndpoint(id string) error {
	if id == DefaultEndpointID {
		return conflict("default endpoint is read-only")
	}
	if s == nil || s.Engine == nil {
		return internal("endpoint service is not initialized", nil)
	}
	s.endpointMu.Lock()
	defer s.endpointMu.Unlock()
	if err := s.Engine.DeleteEndpoint(id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("endpoint not found")
		}
		return internal("delete endpoint", err)
	}
	if s.EndpointRuntime != nil {
		s.EndpointRuntime.RemoveEndpoint(id)
	}
	return nil
}
