package apierror

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// WriteError writes a JSON-formatted error response to the connection
func WriteError(conn net.Conn, message string, code string) error {
	// Set a short timeout for writing the error response
	if err := conn.SetWriteDeadline(time.Now().Add(1 * time.Second)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	resp := ErrorResponse{
		Message: message,
		Code:    code,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal error response: %w", err)
	}

	// Format as HTTP response
	httpResp := fmt.Sprintf("HTTP/1.1 500 Internal Server Error\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"\r\n"+
		"%s", len(data), data)

	// Write the full response in one call to avoid partial writes
	if _, err := conn.Write([]byte(httpResp)); err != nil {
		return fmt.Errorf("failed to write error response: %w", err)
	}

	return nil
}
