package state

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Resinat/Resin/internal/model"
)

func TestStateRepo_EndpointCRUD(t *testing.T) {
	repo := newTestStateRepo(t)
	first := model.Endpoint{
		ID:                   "endpoint-a",
		Port:                 32002,
		Enabled:              true,
		AllowManagement:      true,
		AllowProxy:           true,
		RequireProxyAuthInfo: true,
		AllowHTTPForward:     true,
		AllowHTTPReverse:     false,
		AllowSOCKS5:          true,
		CreatedAtNs:          100,
		UpdatedAtNs:          200,
	}
	second := model.Endpoint{
		ID:               "endpoint-b",
		Port:             32001,
		AllowManagement:  true,
		AllowHTTPReverse: false,
		CreatedAtNs:      300,
		UpdatedAtNs:      300,
	}

	if err := repo.InsertEndpoint(first); err != nil {
		t.Fatalf("InsertEndpoint(first): %v", err)
	}
	if err := repo.InsertEndpoint(second); err != nil {
		t.Fatalf("InsertEndpoint(second): %v", err)
	}

	loaded, err := repo.GetEndpoint(first.ID)
	if err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
	if !reflect.DeepEqual(*loaded, first) {
		t.Fatalf("GetEndpoint = %+v, want %+v", *loaded, first)
	}

	items, err := repo.ListEndpoints()
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	if len(items) != 2 || items[0].ID != second.ID || items[1].ID != first.ID {
		t.Fatalf("ListEndpoints order = %+v, want ports in ascending order", items)
	}

	duplicatePort := first
	duplicatePort.ID = "endpoint-c"
	if err := repo.InsertEndpoint(duplicatePort); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate port error = %v, want ErrConflict", err)
	}

	first.Port = 32003
	first.AllowManagement = false
	first.RequireProxyAuthInfo = false
	first.AllowHTTPReverse = true
	first.AllowSOCKS5 = false
	first.UpdatedAtNs = 400
	if err := repo.UpdateEndpoint(first); err != nil {
		t.Fatalf("UpdateEndpoint: %v", err)
	}
	loaded, err = repo.GetEndpoint(first.ID)
	if err != nil {
		t.Fatalf("GetEndpoint after update: %v", err)
	}
	if !reflect.DeepEqual(*loaded, first) {
		t.Fatalf("updated endpoint = %+v, want %+v", *loaded, first)
	}

	if err := repo.DeleteEndpoint(first.ID); err != nil {
		t.Fatalf("DeleteEndpoint: %v", err)
	}
	if _, err := repo.GetEndpoint(first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetEndpoint after delete error = %v, want ErrNotFound", err)
	}
	if err := repo.DeleteEndpoint(first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteEndpoint error = %v, want ErrNotFound", err)
	}
}
