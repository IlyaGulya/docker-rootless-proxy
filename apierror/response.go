package apierror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// WriteError writes a JSON-formatted error response to the connection
func WriteError(conn net.Conn, message string, code string) error {
	resp := ErrorResponse{
		Message: message,
		Code:    code,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal error response: %w", err)
	}

	// Create HTTP response using net/http
	httpResp := &http.Response{
		Status:     "500 Internal Server Error",
		StatusCode: http.StatusInternalServerError,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Content-Length": []string{fmt.Sprintf("%d", len(data))},
		},
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
	}

	// Write the response
	if err := httpResp.Write(conn); err != nil {
		return fmt.Errorf("failed to write response: %w", err)
	}

	return nil
}
