package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// --- Pagination ---

const (
	defaultPageLimit = 50
	maxPageLimit     = 100000
)

// Pagination holds parsed limit/offset values.
type Pagination struct {
	Limit  int
	Offset int
}

type requestBodyTooLargeError struct {
	Limit int64
}

func (e *requestBodyTooLargeError) Error() string {
	return fmt.Sprintf("request body too large (max %d bytes)", e.Limit)
}

// ParsePagination reads limit and offset from query parameters.
func ParsePagination(r *http.Request) (Pagination, error) {
	p := Pagination{Limit: defaultPageLimit, Offset: 0}

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, fmt.Errorf("limit: must be a non-negative integer")
		}
		if n > maxPageLimit {
			return p, fmt.Errorf("limit: must be <= %d", maxPageLimit)
		}
		if n > 0 {
			p.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return p, fmt.Errorf("offset: must be a non-negative integer")
		}
		p.Offset = n
	}
	return p, nil
}

// --- Sorting ---

// Sorting holds parsed sort_by and sort_order values.
type Sorting struct {
	SortBy    string
	SortOrder string // "asc" or "desc"
}

// ParseSorting reads sort_by and sort_order from query parameters.
func ParseSorting(r *http.Request, allowed []string, defaultField, defaultOrder string) (Sorting, error) {
	s := Sorting{SortBy: defaultField, SortOrder: defaultOrder}

	if v := r.URL.Query().Get("sort_by"); v != "" {
		if !slices.Contains(allowed, v) {
			return s, fmt.Errorf("sort_by: must be one of %v", allowed)
		}
		s.SortBy = v
	}
	if v := r.URL.Query().Get("sort_order"); v != "" {
		v = strings.ToLower(v)
		if v != "asc" && v != "desc" {
			return s, fmt.Errorf("sort_order: must be 'asc' or 'desc'")
		}
		s.SortOrder = v
	}
	return s, nil
}

// --- Body Decoding ---

// DecodeBody decodes the JSON request body into v, rejecting unknown fields.
func DecodeBody(r *http.Request, v interface{}) error {
	if r.Body == nil {
		return fmt.Errorf("request body is required")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &requestBodyTooLargeError{Limit: maxErr.Limit}
		}
		return fmt.Errorf("invalid request body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &requestBodyTooLargeError{Limit: maxErr.Limit}
		}
		return fmt.Errorf("invalid request body: must contain a single JSON value")
	}
	return nil
}

// --- Path Parameters ---

// PathParam extracts a named path parameter from the request URL.
// Works with Go 1.22+ ServeMux pattern matching (e.g. /platforms/{id}).
func PathParam(r *http.Request, name string) string {
	return r.PathValue(name)
}

// --- Query Parameters ---

// ParseBoolQuery parses an optional boolean query parameter.
// Returns nil when the parameter is not present.
func ParseBoolQuery(r *http.Request, key string) (*bool, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, fmt.Errorf("%s: must be true or false", key)
	}
	return &b, nil
}

// --- Validators ---

// ValidateUUID checks that s is a valid lowercase canonical UUID string.
func ValidateUUID(s string) bool {
	id, err := uuid.Parse(s)
	if err != nil {
		return false
	}
	return s == id.String()
}

// PaginateSlice applies limit/offset to a slice and returns the page.
func PaginateSlice[T any](items []T, p Pagination) []T {
	if p.Offset >= len(items) {
		return []T{}
	}
	end := p.Offset + p.Limit
	if end > len(items) {
		end = len(items)
	}
	return items[p.Offset:end]
}

// --- Sort Slice ---

// SortSlice sorts items in place by the given key extractor and sort order.
// keyFn must return a comparable string value for sorting.
func SortSlice[T any](items []T, sort Sorting, keyFn func(T) string) {
	if sort.SortBy == "" || len(items) == 0 {
		return
	}
	if sort.SortOrder == "desc" {
		slices.SortStableFunc(items, func(a, b T) int {
			return strings.Compare(keyFn(b), keyFn(a))
		})
	} else {
		slices.SortStableFunc(items, func(a, b T) int {
			return strings.Compare(keyFn(a), keyFn(b))
		})
	}
}
