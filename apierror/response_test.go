package apierror

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

type mockConn struct {
	net.Conn
	buffer bytes.Buffer
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
	return m.buffer.Write(p)
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

			// Parse the HTTP response
			resp, err := http.ReadResponse(bufio.NewReader(&conn.buffer), nil)
			if err != nil {
				t.Fatalf("Failed to parse HTTP response: %v", err)
			}
			defer resp.Body.Close()

			// Verify response status and headers
			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			// Read and parse response body
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			var errorResp ErrorResponse
			if err := json.Unmarshal(body, &errorResp); err != nil {
				t.Fatalf("Failed to parse response JSON: %v", err)
			}

			if errorResp.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", errorResp.Message, tt.wantMessage)
			}
			if errorResp.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", errorResp.Code, tt.wantCode)
			}
		})
	}
}
