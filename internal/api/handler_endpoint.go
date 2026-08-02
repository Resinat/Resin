package api

import (
	"net/http"

	"github.com/Resinat/Resin/internal/service"
)

func HandleListEndpoints(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		endpoints, err := cp.ListEndpoints()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WritePage(w, http.StatusOK, endpoints, pg)
	}
}

func HandleGetEndpoint(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		endpoint, err := cp.GetEndpoint(PathParam(r, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, endpoint)
	}
}

func HandleCreateEndpoint(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.CreateEndpointRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		endpoint, err := cp.CreateEndpoint(req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, endpoint)
	}
}

func HandleUpdateEndpoint(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readRawBodyOrWriteInvalid(w, r)
		if !ok {
			return
		}
		endpoint, err := cp.UpdateEndpoint(PathParam(r, "id"), body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, endpoint)
	}
}

func HandleDeleteEndpoint(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := cp.DeleteEndpoint(PathParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
