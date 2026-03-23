package errors

import (
	"fmt"
	"net/http"
)

// ErrorCode represents a categorized error type for structured error handling.
type ErrorCode string

const (
	ErrInvalidInput    ErrorCode = "INVALID_INPUT"
	ErrNotFound        ErrorCode = "NOT_FOUND"
	ErrConflict        ErrorCode = "CONFLICT"
	ErrResourceLimit   ErrorCode = "RESOURCE_LIMIT"
	ErrDownloadFailed  ErrorCode = "DOWNLOAD_FAILED"
	ErrChecksumMismatch ErrorCode = "CHECKSUM_MISMATCH"
	ErrProcessFailed   ErrorCode = "PROCESS_FAILED"
	ErrNetworkSetup    ErrorCode = "NETWORK_SETUP"
	ErrInternal        ErrorCode = "INTERNAL"
	ErrTimeout         ErrorCode = "TIMEOUT"
	ErrRateLimited     ErrorCode = "RATE_LIMITED"
)

// SistemoError is a structured error carrying a machine-readable code,
// a human-readable message, and an optional wrapped cause.
type SistemoError struct {
	Code    ErrorCode
	Message string
	Err     error
}

// Error implements the error interface.
func (e *SistemoError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause, enabling errors.Is / errors.As chains.
func (e *SistemoError) Unwrap() error {
	return e.Err
}

// Newf creates a new SistemoError with a formatted message and no wrapped cause.
func Newf(code ErrorCode, format string, args ...interface{}) *SistemoError {
	return &SistemoError{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap creates a new SistemoError that wraps an existing error.
func Wrap(code ErrorCode, msg string, err error) *SistemoError {
	return &SistemoError{
		Code:    code,
		Message: msg,
		Err:     err,
	}
}

// ToHTTPStatus maps the error code to an appropriate HTTP status code.
func (e *SistemoError) ToHTTPStatus() int {
	switch e.Code {
	case ErrInvalidInput:
		return http.StatusBadRequest // 400
	case ErrNotFound:
		return http.StatusNotFound // 404
	case ErrConflict:
		return http.StatusConflict // 409
	case ErrResourceLimit:
		return http.StatusUnprocessableEntity // 422
	case ErrDownloadFailed:
		return http.StatusBadGateway // 502
	case ErrChecksumMismatch:
		return http.StatusUnprocessableEntity // 422
	case ErrProcessFailed:
		return http.StatusInternalServerError // 500
	case ErrNetworkSetup:
		return http.StatusInternalServerError // 500
	case ErrInternal:
		return http.StatusInternalServerError // 500
	case ErrTimeout:
		return http.StatusGatewayTimeout // 504
	case ErrRateLimited:
		return http.StatusTooManyRequests // 429
	default:
		return http.StatusInternalServerError // 500
	}
}
