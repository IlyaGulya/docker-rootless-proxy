package apierror

import "fmt"

// ErrorResponse matches the Docker API error response format
type ErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Common error codes that match Docker API conventions
const (
	CodeSocketNotFound   = "socket_not_found"
	CodePermissionDenied = "permission_denied"
	CodeConnectionFailed = "connection_failed"
	CodeInternalError    = "internal_error"
)

// NewSocketNotFoundError returns an error response for missing socket
func NewSocketNotFoundError(socketPath string) *ErrorResponse {
	return &ErrorResponse{
		Message: fmt.Sprintf("cannot connect to Docker daemon at %s", socketPath),
		Code:    CodeSocketNotFound,
	}
}

// NewPermissionDeniedError returns an error response for permission issues
func NewPermissionDeniedError() *ErrorResponse {
	return &ErrorResponse{
		Message: "permission denied while trying to connect to Docker daemon",
		Code:    CodePermissionDenied,
	}
}

// NewConnectionFailedError returns an error response for general connection failures
func NewConnectionFailedError(err error) *ErrorResponse {
	return &ErrorResponse{
		Message: fmt.Sprintf("error while connecting to Docker daemon: %v", err),
		Code:    CodeConnectionFailed,
	}
}
