// Package api implements the HTTP API server for Resin.
package api

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error code and human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError writes a standard error response.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// PageResponse is the standard list envelope for paginated endpoints.
type PageResponse[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// WritePage writes a paginated list response.
func WritePage[T any](w http.ResponseWriter, status int, allItems []T, p Pagination) {
	WriteJSON(w, status, PageResponse[T]{
		Items:  PaginateSlice(allItems, p),
		Total:  len(allItems),
		Limit:  p.Limit,
		Offset: p.Offset,
	})
}
