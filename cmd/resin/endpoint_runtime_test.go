package main

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/state"
)

func TestEndpointRuntimeManager_RemoveReleasesPortBeforeReturn(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatalf("release test port: %v", err)
	}

	manager := newEndpointRuntimeManager("127.0.0.1", "", nil, nil, nil, nil, nil, nil)
	endpoint := model.Endpoint{ID: "custom", Port: port, Enabled: true}
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("first ApplyEndpoint: %v", err)
	}
	manager.RemoveEndpoint(endpoint.ID)
	if err := manager.ApplyEndpoint(endpoint); err != nil {
		t.Fatalf("ApplyEndpoint immediately after RemoveEndpoint: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestRestorePersistedEndpoints_SkipsDisabledListeners(t *testing.T) {
	ports := reserveTestPorts(t, 2)
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	for _, endpoint := range []model.Endpoint{
		{ID: "enabled", Port: ports[0], Enabled: true},
		{ID: "disabled", Port: ports[1], Enabled: false},
	} {
		if err := engine.InsertEndpoint(endpoint); err != nil {
			t.Fatalf("InsertEndpoint(%s): %v", endpoint.ID, err)
		}
	}

	manager := newEndpointRuntimeManager("127.0.0.1", "", nil, nil, nil, nil, nil, nil)
	if err := restorePersistedEndpoints(engine, manager); err != nil {
		t.Fatalf("restorePersistedEndpoints: %v", err)
	}
	if status := manager.EndpointStatus("enabled"); status.State != "starting" {
		t.Fatalf("enabled endpoint status = %+v, want starting", status)
	}
	if status := manager.EndpointStatus("disabled"); status.State != "inactive" {
		t.Fatalf("disabled endpoint status = %+v, want inactive", status)
	}

	disabledListener, err := net.Listen("tcp", formatListenAddress("127.0.0.1", ports[1]))
	if err != nil {
		t.Fatalf("disabled endpoint port should remain available: %v", err)
	}
	_ = disabledListener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func reserveTestPorts(t *testing.T, count int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, count)
	for range count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve test port: %v", err)
		}
		listeners = append(listeners, listener)
	}
	ports := make([]int, 0, count)
	for _, listener := range listeners {
		ports = append(ports, listener.Addr().(*net.TCPAddr).Port)
		if err := listener.Close(); err != nil {
			t.Fatalf("release test port: %v", err)
		}
	}
	return ports
}
