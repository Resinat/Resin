package service

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/state"
)

type endpointRuntimeStub struct {
	endpoints map[string]model.Endpoint
	failPort  int
}

func newEndpointRuntimeStub() *endpointRuntimeStub {
	return &endpointRuntimeStub{endpoints: make(map[string]model.Endpoint)}
}

func (r *endpointRuntimeStub) ApplyEndpoint(endpoint model.Endpoint) error {
	if endpoint.Port == r.failPort {
		return errors.New("address already in use")
	}
	r.endpoints[endpoint.ID] = endpoint
	return nil
}

func (r *endpointRuntimeStub) RemoveEndpoint(id string) {
	delete(r.endpoints, id)
}

func (r *endpointRuntimeStub) EndpointStatus(id string) EndpointRuntimeStatus {
	if _, ok := r.endpoints[id]; ok {
		return EndpointRuntimeStatus{State: "active"}
	}
	return EndpointRuntimeStatus{State: "inactive"}
}

func newEndpointTestService(t *testing.T, env *config.EnvConfig) (*ControlPlaneService, *endpointRuntimeStub) {
	t.Helper()
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	runtime := newEndpointRuntimeStub()
	cp := &ControlPlaneService{Engine: engine, EnvCfg: env, EndpointRuntime: runtime}
	if err := runtime.ApplyEndpoint(cp.defaultEndpoint()); err != nil {
		t.Fatalf("apply default endpoint: %v", err)
	}
	return cp, runtime
}

func boolPointer(value bool) *bool { return &value }

func TestNewDefaultEndpoint(t *testing.T) {
	endpoint := NewDefaultEndpoint(0)
	if endpoint.ID != DefaultEndpointID || endpoint.Port != 2260 ||
		!endpoint.Enabled ||
		!endpoint.AllowManagement || !endpoint.AllowProxy ||
		!endpoint.AllowHTTPForward || !endpoint.AllowHTTPReverse || !endpoint.AllowSOCKS5 {
		t.Fatalf("default endpoint = %+v", endpoint)
	}
}

func TestControlPlaneEndpoints_CRUDAndDefaultProtection(t *testing.T) {
	cp, runtime := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		AuthVersion: config.AuthVersionV1,
	})

	items, err := cp.ListEndpoints()
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	if len(items) != 1 || items[0].ID != DefaultEndpointID || !items[0].ReadOnly || !items[0].AllowSOCKS5 {
		t.Fatalf("default endpoint = %+v", items)
	}

	created, err := cp.CreateEndpoint(CreateEndpointRequest{
		Port:                 32020,
		AllowManagement:      boolPointer(true),
		AllowProxy:           boolPointer(true),
		RequireProxyAuthInfo: boolPointer(true),
		AllowHTTPForward:     boolPointer(true),
		AllowHTTPReverse:     boolPointer(false),
		AllowSOCKS5:          boolPointer(false),
	})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if created.ReadOnly || !created.Enabled || created.Status != "active" || !created.RequireProxyAuthInfo {
		t.Fatalf("created endpoint = %+v", created)
	}
	if runtime.endpoints[created.ID].Port != 32020 {
		t.Fatalf("runtime endpoint = %+v", runtime.endpoints[created.ID])
	}

	updated, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{
		"port": 32021,
		"allow_management": false,
		"require_proxy_auth_info": false,
		"allow_http_reverse": true
	}`))
	if err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	if updated.Port != 32021 || updated.AllowManagement || updated.RequireProxyAuthInfo || !updated.AllowHTTPReverse {
		t.Fatalf("updated endpoint = %+v", updated)
	}

	if _, err := cp.UpdateEndpoint(DefaultEndpointID, json.RawMessage(`{"port": 32022}`)); err == nil {
		t.Fatal("UpdateEndpoint(default) succeeded, want conflict")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}
	if err := cp.DeleteEndpoint(DefaultEndpointID); err == nil {
		t.Fatal("DeleteEndpoint(default) succeeded, want conflict")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}

	if err := cp.DeleteEndpoint(created.ID); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	if _, ok := runtime.endpoints[created.ID]; ok {
		t.Fatal("runtime endpoint still exists after delete")
	}
	if _, err := cp.GetEndpoint(created.ID); err == nil {
		t.Fatal("GetEndpoint after delete succeeded")
	} else {
		assertServiceErrorCode(t, err, "NOT_FOUND")
	}
}

func TestControlPlaneEndpoints_RequireAuthInfoCanBeConfiguredWithProxyToken(t *testing.T) {
	cp, _ := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		ProxyToken:  "secret",
		AuthVersion: config.AuthVersionV1,
	})
	created, err := cp.CreateEndpoint(CreateEndpointRequest{
		Port:                 32030,
		RequireProxyAuthInfo: boolPointer(true),
	})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if !created.RequireProxyAuthInfo {
		t.Fatalf("require_proxy_auth_info = false, want true")
	}
	updated, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{
		"port": 32031,
		"require_proxy_auth_info": true
	}`))
	if err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	if updated.Port != 32031 || !updated.RequireProxyAuthInfo {
		t.Fatalf("updated endpoint = %+v", updated)
	}
}

func TestControlPlaneEndpoints_ManagementOnlyDefaultsProxyProtocolsOff(t *testing.T) {
	cp, _ := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		AuthVersion: config.AuthVersionV1,
	})
	created, err := cp.CreateEndpoint(CreateEndpointRequest{
		Port:            32032,
		AllowManagement: boolPointer(true),
		AllowProxy:      boolPointer(false),
	})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if created.AllowProxy || created.AllowHTTPForward || created.AllowHTTPReverse || created.AllowSOCKS5 {
		t.Fatalf("management-only endpoint has proxy capability enabled: %+v", created)
	}
}

func TestControlPlaneEndpoints_CreateDisabled(t *testing.T) {
	cp, runtime := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		AuthVersion: config.AuthVersionV1,
	})
	created, err := cp.CreateEndpoint(CreateEndpointRequest{
		Port:    32033,
		Enabled: boolPointer(false),
	})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	if created.Enabled || created.Status != "inactive" {
		t.Fatalf("disabled endpoint = %+v", created)
	}
	if _, ok := runtime.endpoints[created.ID]; ok {
		t.Fatal("disabled endpoint was applied to runtime")
	}
	persisted, err := cp.Engine.GetEndpoint(created.ID)
	if err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
	if persisted.Enabled {
		t.Fatal("disabled create state was not persisted")
	}
}

func TestControlPlaneEndpoints_ListenerFailureRollsBackPersistence(t *testing.T) {
	cp, runtime := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		AuthVersion: config.AuthVersionV1,
	})

	runtime.failPort = 32040
	if _, err := cp.CreateEndpoint(CreateEndpointRequest{Port: 32040}); err == nil {
		t.Fatal("CreateEndpoint succeeded despite listener failure")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}
	items, err := cp.Engine.ListEndpoints()
	if err != nil {
		t.Fatalf("ListEndpoints after failed create: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("persisted endpoints after failed create = %+v", items)
	}

	runtime.failPort = 0
	created, err := cp.CreateEndpoint(CreateEndpointRequest{Port: 32041})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	runtime.failPort = 32042
	if _, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"port":32042}`)); err == nil {
		t.Fatal("UpdateEndpoint succeeded despite listener failure")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}
	persisted, err := cp.Engine.GetEndpoint(created.ID)
	if err != nil {
		t.Fatalf("GetEndpoint after failed update: %v", err)
	}
	if persisted.Port != 32041 {
		t.Fatalf("persisted port after failed update = %d, want 32041", persisted.Port)
	}
}

func TestControlPlaneEndpoints_EnableAndDisableWithPatch(t *testing.T) {
	cp, runtime := newEndpointTestService(t, &config.EnvConfig{
		ResinPort:   2260,
		AuthVersion: config.AuthVersionV1,
	})
	created, err := cp.CreateEndpoint(CreateEndpointRequest{Port: 32060})
	if err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	disabled, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":false}`))
	if err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if disabled.Enabled || disabled.Status != "inactive" {
		t.Fatalf("disabled endpoint = %+v", disabled)
	}
	if _, ok := runtime.endpoints[created.ID]; ok {
		t.Fatal("disabled endpoint still exists in runtime")
	}
	persisted, err := cp.Engine.GetEndpoint(created.ID)
	if err != nil {
		t.Fatalf("GetEndpoint after disable: %v", err)
	}
	if persisted.Enabled {
		t.Fatal("disabled state was not persisted")
	}

	// A disabled endpoint can be reconfigured without trying to bind its port.
	runtime.failPort = 32061
	updated, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"port":32061}`))
	if err != nil {
		t.Fatalf("UpdateEndpoint while disabled: %v", err)
	}
	if updated.Enabled || updated.Port != 32061 || updated.Status != "inactive" {
		t.Fatalf("updated disabled endpoint = %+v", updated)
	}

	if _, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":true}`)); err == nil {
		t.Fatal("enabling endpoint succeeded despite listener failure")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}
	persisted, err = cp.Engine.GetEndpoint(created.ID)
	if err != nil {
		t.Fatalf("GetEndpoint after failed start: %v", err)
	}
	if persisted.Enabled {
		t.Fatal("failed start should roll enabled state back to false")
	}

	runtime.failPort = 0
	started, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("enable endpoint: %v", err)
	}
	if !started.Enabled || started.Status != "active" || started.Port != 32061 {
		t.Fatalf("started endpoint = %+v", started)
	}
	if _, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatalf("second enable patch should be idempotent: %v", err)
	}
	if _, err := cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatalf("second disable patch: %v", err)
	}
	if disabled, err = cp.UpdateEndpoint(created.ID, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatalf("third disable patch should be idempotent: %v", err)
	} else if disabled.Enabled || disabled.Status != "inactive" {
		t.Fatalf("idempotently disabled endpoint = %+v", disabled)
	}

	if _, err := cp.UpdateEndpoint(DefaultEndpointID, json.RawMessage(`{"enabled":false}`)); err == nil {
		t.Fatal("disabling default endpoint succeeded")
	} else {
		assertServiceErrorCode(t, err, "CONFLICT")
	}
}
