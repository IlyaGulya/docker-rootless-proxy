package apierror

import (
	"errors"
	"strings"
	"testing"
)

func TestErrorResponses(t *testing.T) {
	t.Run("socket_not_found", func(t *testing.T) {
		socketPath := "/path/to/socket"
		resp := NewSocketNotFoundError(socketPath)
		if !strings.Contains(resp.Message, socketPath) {
			t.Error("Socket path not included in error message")
		}
		if resp.Code != CodeSocketNotFound {
			t.Errorf("Code = %q, want %q", resp.Code, CodeSocketNotFound)
		}
	})

	t.Run("permission_denied", func(t *testing.T) {
		resp := NewPermissionDeniedError()
		if !strings.Contains(resp.Message, "permission denied") {
			t.Error("Message should contain 'permission denied'")
		}
		if resp.Code != CodePermissionDenied {
			t.Errorf("Code = %q, want %q", resp.Code, CodePermissionDenied)
		}
	})

	t.Run("connection_failed", func(t *testing.T) {
		testErr := errors.New("connection refused")
		resp := NewConnectionFailedError(testErr)
		if !strings.Contains(resp.Message, testErr.Error()) {
			t.Error("Original error not included in message")
		}
		if resp.Code != CodeConnectionFailed {
			t.Errorf("Code = %q, want %q", resp.Code, CodeConnectionFailed)
		}
	})
}
