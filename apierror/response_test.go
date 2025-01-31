package apierror

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

type mockConn struct {
	net.Conn
	written []byte
}

// Provide a no-op or a mock implementation:
func (m *mockConn) SetWriteDeadline(t time.Time) error {
	return nil
}
func (m *mockConn) SetReadDeadline(t time.Time) error {
	return nil
}
func (m *mockConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) Write(p []byte) (n int, err error) {
	m.written = append(m.written, p...)
	return len(p), nil
}

func (m *mockConn) Close() error { return nil }

func TestWriteError(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		code        string
		wantMessage string
		wantCode    string
	}{
		{
			name:        "socket_not_found",
			message:     "cannot connect to Docker daemon at unix:///path/to/socket",
			code:        CodeSocketNotFound,
			wantMessage: "cannot connect to Docker daemon at unix:///path/to/socket",
			wantCode:    CodeSocketNotFound,
		},
		{
			name:        "permission_denied",
			message:     "permission denied",
			code:        CodePermissionDenied,
			wantMessage: "permission denied",
			wantCode:    CodePermissionDenied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &mockConn{}

			err := WriteError(conn, tt.message, tt.code)
			if err != nil {
				t.Fatalf("WriteError() error = %v", err)
			}

			// Verify HTTP response format
			response := string(conn.written)
			if !strings.HasPrefix(response, "HTTP/1.1 500 Internal Server Error\r\n") {
				t.Error("Response missing HTTP status line")
			}
			if !strings.Contains(response, "Content-Type: application/json") {
				t.Error("Response missing Content-Type header")
			}

			// Extract and parse JSON body
			parts := strings.Split(response, "\r\n\r\n")
			if len(parts) != 2 {
				t.Fatal("Invalid response format")
			}

			var resp ErrorResponse
			if err := json.Unmarshal([]byte(parts[1]), &resp); err != nil {
				t.Fatalf("Failed to parse response JSON: %v", err)
			}

			if resp.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", resp.Message, tt.wantMessage)
			}
			if resp.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", resp.Code, tt.wantCode)
			}
		})
	}
}
