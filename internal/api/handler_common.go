package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

func parsePaginationOrWriteInvalid(w http.ResponseWriter, r *http.Request) (Pagination, bool) {
	pg, err := ParsePagination(r)
	if err != nil {
		writeInvalidArgument(w, err.Error())
		return Pagination{}, false
	}
	return pg, true
}

func parseSortingOrWriteInvalid(
	w http.ResponseWriter,
	r *http.Request,
	allowed []string,
	defaultField string,
	defaultOrder string,
) (Sorting, bool) {
	s, err := ParseSorting(r, allowed, defaultField, defaultOrder)
	if err != nil {
		writeInvalidArgument(w, err.Error())
		return Sorting{}, false
	}
	return s, true
}

func parseBoolQueryOrWriteInvalid(w http.ResponseWriter, r *http.Request, key string) (*bool, bool) {
	v, err := ParseBoolQuery(r, key)
	if err != nil {
		writeInvalidArgument(w, err.Error())
		return nil, false
	}
	return v, true
}

func readRawBodyOrWriteInvalid(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		writeInvalidArgument(w, "request body is required")
		return nil, false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writePayloadTooLarge(w, maxErr.Limit)
			return nil, false
		}
		writeInvalidArgument(w, "failed to read body")
		return nil, false
	}
	return body, true
}

func requireUUIDPathParam(
	w http.ResponseWriter,
	r *http.Request,
	paramName string,
	fieldName string,
) (string, bool) {
	value := PathParam(r, paramName)
	if !ValidateUUID(value) {
		writeInvalidArgument(w, fmt.Sprintf("%s: must be a valid UUID", fieldName))
		return "", false
	}
	return value, true
}

func parseOptionalUUIDQuery(
	w http.ResponseWriter,
	r *http.Request,
	queryKey string,
	fieldName string,
) (*string, bool) {
	value := r.URL.Query().Get(queryKey)
	if value == "" {
		return nil, true
	}
	if !ValidateUUID(value) {
		writeInvalidArgument(w, fmt.Sprintf("%s: must be a valid UUID", fieldName))
		return nil, false
	}
	return &value, true
}
