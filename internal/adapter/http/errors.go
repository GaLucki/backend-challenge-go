package httpadapter

import (
	"encoding/json"
	"net/http"
)

// ErrorCode identifies a stable API error category.
type ErrorCode string

const (
	ErrorCodeInternal     ErrorCode = "INTERNAL_ERROR"
	ErrorCodeNotFound     ErrorCode = "NOT_FOUND"
	ErrorCodeNotReady     ErrorCode = "NOT_READY"
	ErrorCodeUnauthorized ErrorCode = "UNAUTHORIZED"
	ErrorCodeForbidden    ErrorCode = "FORBIDDEN"
)

// APIError is the standard error payload returned by the HTTP API.
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type errorResponse struct {
	Error APIError `json:"error"`
}

// WriteError writes a consistent JSON error response.
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(errorResponse{
		Error: APIError{
			Code:    code,
			Message: message,
		},
	})
}

// WriteJSON writes a JSON success response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
