package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/resin-proxy/resin/internal/service"
)

func invalidArgumentError(message string) *service.ServiceError {
	return &service.ServiceError{
		Code:    "INVALID_ARGUMENT",
		Message: message,
	}
}

func writeInvalidArgument(w http.ResponseWriter, message string) {
	writeServiceError(w, invalidArgumentError(message))
}

func writePayloadTooLarge(w http.ResponseWriter, limit int64) {
	msg := "request body too large"
	if limit > 0 {
		msg = "request body too large (max " + strconv.FormatInt(limit, 10) + " bytes)"
	}
	WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", msg)
}

func writeDecodeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *requestBodyTooLargeError
	if errors.As(err, &tooLarge) {
		writePayloadTooLarge(w, tooLarge.Limit)
		return
	}
	writeInvalidArgument(w, err.Error())
}

// writeServiceError maps service errors to HTTP response codes.
func writeServiceError(w http.ResponseWriter, err error) {
	if err == nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
		return
	}

	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		var status int
		switch svcErr.Code {
		case "INVALID_ARGUMENT":
			status = http.StatusBadRequest
		case "NOT_FOUND":
			status = http.StatusNotFound
		case "CONFLICT":
			status = http.StatusConflict
		default:
			status = http.StatusInternalServerError
		}
		WriteError(w, status, svcErr.Code, svcErr.Message)
		return
	}
	WriteError(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
}
