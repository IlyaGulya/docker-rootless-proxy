package router

import (
	"bufio"
	"context"
	"docker-socket-router/apierror"
	"docker-socket-router/config"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"

	"go.uber.org/zap"
)

// TestRouterGetUserUid verifies that the router can correctly retrieve the user's UID.
func TestRouterGetUserUid(t *testing.T) {
	withTestLogger(t, func(logger *zap.Logger) {
		tempDir := t.TempDir()
		socketPath := filepath.Join(tempDir, "test.sock")

		cfg := &config.SocketConfig{
			SystemSocket:         socketPath,
			RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
		}

		router := NewRouter(logger, cfg, NewDefaultDialer(), newSocketManager(socketPath))

		t.Run("valid_connection", func(t *testing.T) {
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatalf("Failed to create test socket: %v", err)
			}
			defer listener.Close()

			accepted := make(chan net.Conn)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					t.Errorf("Accept error: %v", err)
					close(accepted)
					return
				}
				accepted <- conn
			}()

			conn, err := net.Dial("unix", socketPath)
			if err != nil {
				t.Fatalf("Failed to connect to test socket: %v", err)
			}
			defer conn.Close()

			uid, err := router.getUserUid(conn)
			if err != nil {
				t.Fatalf("Failed to get UID: %v", err)
			}

			expectedUid := uint32(os.Getuid())
			if uid != expectedUid {
				t.Errorf("Expected UID %d, got %d", expectedUid, uid)
			}

			if acceptedConn := <-accepted; acceptedConn != nil {
				acceptedConn.Close()
			}
		})
	})
}

// TestRouterLifecycle verifies that the router starts/stops cleanly
// (creating and removing the system socket and associated PID file).
func TestRouterLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	// Ensure cleanup of any stale files
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	// Clean up any stale files before each test
	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, 0755)

	t.Run("normal_lifecycle", func(t *testing.T) {
		// Use our TestAppBuilder helper with test socket manager.
		builder := NewTestAppBuilder(t, cfg)
		stopRouter := builder.Start(context.Background())

		// Add a defer to ensure cleanup even if test fails
		defer func() {
			stopRouter()
			os.Remove(cfg.SystemSocket)
			os.Remove(cfg.SystemSocket + ".pid")
		}()

		// Add a small delay to ensure socket is ready
		time.Sleep(100 * time.Millisecond)

		// Verify that the socket file exists with the proper permissions.
		info, err := os.Stat(cfg.SystemSocket)
		if err != nil {
			t.Fatalf("Socket not created: %v", err)
		}
		if info.Mode()&os.ModePerm != 0666 {
			t.Errorf("Expected socket permissions 0666, got %v", info.Mode()&os.ModePerm)
		}

		// Verify that the PID file exists and contains our PID.
		pidData, err := os.ReadFile(cfg.SystemSocket + ".pid")
		if err != nil {
			t.Fatalf("PID file not created: %v", err)
		}
		pidStr := strings.TrimSpace(string(pidData))
		pidVal, err := strconv.Atoi(pidStr)
		if err != nil {
			t.Fatalf("Invalid PID file content: %v", err)
		}
		if pidVal != os.Getpid() {
			t.Errorf("Expected PID %d, got %d", os.Getpid(), pidVal)
		}
	})

	t.Run("failed_socket_creation", func(t *testing.T) {
		// Create a directory with the same name as the socket.
		if err := os.Mkdir(cfg.SystemSocket, 0755); err != nil {
			t.Fatal(err)
		}

		// Build the Fx app directly (without fxtest.New) so we can capture the error.
		app := fx.New(
			fx.Provide(
				func() *zap.Logger { return zap.NewExample() },
				func() *config.SocketConfig { return cfg },
				func() Dialer { return NewDefaultDialer() },
				func(logger *zap.Logger, cfg *config.SocketConfig, dialer Dialer) *Router {
					// Use regular socket manager here since we want to test the failure case
					return NewRouter(logger, cfg, dialer, newSocketManager(cfg.SystemSocket))
				},
			),
			fx.Invoke(func(lc fx.Lifecycle, r *Router) error {
				return r.Start(lc)
			}),
		)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		err := app.Start(ctx)
		if err == nil {
			t.Error("Expected error on start due to socket creation failure, got nil")
		}
		// Stop the app even though startup failed.
		_ = app.Stop(ctx)
	})
}

// TestIntegration - Example integration test verifying that the router
// correctly forwards data between the system socket and a mock rootless socket.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	uid := os.Getuid()

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	// Create a mock Docker user socket.
	userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)
	userListener, err := net.Listen("unix", userSocket)
	if err != nil {
		t.Fatalf("Failed to create user socket: %v", err)
	}
	defer userListener.Close()

	// Basic echo server.
	go func() {
		for {
			conn, err := userListener.Accept()
			if err != nil {
				return
			}
			go echo(conn)
		}
	}()

	// Start the router using our TestAppBuilder.
	builder := NewTestAppBuilder(t, cfg)
	stopRouter := builder.Start(context.Background())
	defer stopRouter()

	t.Run("single_connection", func(t *testing.T) {
		if err := testConnection(t, cfg.SystemSocket); err != nil {
			t.Errorf("single_connection test error: %v", err)
		}
	})

	t.Run("concurrent_connections", func(t *testing.T) {
		var wg sync.WaitGroup
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := testConnection(t, cfg.SystemSocket); err != nil {
					errors <- err
				}
			}()
		}

		go func() {
			wg.Wait()
			close(errors)
		}()

		for err := range errors {
			t.Errorf("Concurrent connection error: %v", err)
		}
	})
}

// testConnection attempts a round‐trip to verify the router forwards data.
func testConnection(t *testing.T, socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	testData := []byte("test message")
	if _, err := conn.Write(testData); err != nil {
		return fmt.Errorf("failed to write: %w", err)
	}

	buf := make([]byte, len(testData))
	if _, err := conn.Read(buf); err != nil {
		return fmt.Errorf("failed to read: %w", err)
	}
	if string(buf) != string(testData) {
		return fmt.Errorf("expected %q, got %q", testData, buf)
	}
	return nil
}

func echo(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	conn.Write(buf[:n])
}

// TestRouterErrorResponseMissingSocket checks that if the user socket is NOT present,
// the router returns an HTTP 500 JSON error with CodeSocketNotFound.
func TestRouterErrorResponseMissingSocket(t *testing.T) {
	tempDir := t.TempDir()
	uid := os.Getuid()

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	// Start the router.
	builder := NewTestAppBuilder(t, cfg)
	stopRouter := builder.Start(context.Background())
	defer stopRouter()

	// Allow router to initialize.
	time.Sleep(100 * time.Millisecond)

	// Create a connection with keepalive to prevent early closure.
	dialer := &net.Dialer{
		Timeout:   1 * time.Second,
		KeepAlive: 100 * time.Millisecond,
	}

	conn, err := dialer.Dial("unix", cfg.SystemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	// Ensure connection cleanup.
	defer func() {
		conn.SetDeadline(time.Now().Add(100 * time.Millisecond))
		conn.Close()
	}()

	// Set a reasonable deadline for the entire operation.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("Failed to set deadline: %v", err)
	}

	// Send a proper HTTP request.
	request := "GET /version HTTP/1.1\r\nHost: unix\r\nConnection: keep-alive\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("Failed to write request: %v", err)
	}

	// Read the response with retries.
	reader := bufio.NewReader(conn)

	var statusLine string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statusLine, err = reader.ReadString('\n')
		if err == nil {
			break
		}
		if err != io.EOF {
			t.Fatalf("Failed to read status line: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.HasPrefix(statusLine, "HTTP/1.1 500") {
		t.Fatalf("Expected HTTP 500 response, got: %s", statusLine)
	}

	// Read headers.
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read header line: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // End of headers.
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			lengthStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:"))
			contentLength, err = strconv.Atoi(lengthStr)
			if err != nil {
				t.Fatalf("Invalid Content-Length: %v", err)
			}
		}
	}

	if contentLength <= 0 {
		t.Fatal("Missing or invalid Content-Length")
	}

	// Read the response body.
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}

	var errorResp apierror.ErrorResponse
	if err := json.Unmarshal(body, &errorResp); err != nil {
		t.Fatalf("Failed to parse error JSON: %v\nBody: %s", err, string(body))
	}

	expectedSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)
	expectedMsg := fmt.Sprintf("Cannot connect to Docker daemon at %s", expectedSocket)

	if errorResp.Code != apierror.CodeSocketNotFound {
		t.Errorf("Expected error code %q, got %q", apierror.CodeSocketNotFound, errorResp.Code)
	}
	if errorResp.Message != expectedMsg {
		t.Errorf("Expected message %q, got %q", expectedMsg, errorResp.Message)
	}
}

// TestRouterErrorResponsePermissionDenied checks that if the router tries to connect
// to a user socket but lacks permissions, we get an HTTP 500 JSON error with CodePermissionDenied.
func TestRouterErrorResponsePermissionDenied(t *testing.T) {
	// Skip if running as root because root can usually connect.
	if os.Geteuid() == 0 {
		t.Skip("Skipping permission_denied test because running as root will almost never EACCES.")
	}

	// Create a mock dialer that always returns a permission error.
	permErr := os.ErrPermission
	mock := &mockDialer{err: permErr}

	tempDir := t.TempDir()

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	// We do not need to actually create a user socket file because our mock dialer
	// simulates the dial error.
	builder := NewTestAppBuilder(t, cfg).WithDialer(mock)
	stopRouter := builder.Start(context.Background())
	defer stopRouter()

	// Allow router to fully initialize.
	time.Sleep(100 * time.Millisecond)

	// Connect to the router.
	dialer := net.Dialer{Timeout: 1 * time.Second}
	conn, err := dialer.Dial("unix", cfg.SystemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	defer conn.Close()

	// Set deadline for the entire operation.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("Failed to set deadline: %v", err)
	}

	// Write something to trigger handleConnection.
	if _, err := conn.Write([]byte("GET /version HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatalf("Failed to write request: %v", err)
	}

	// Read and verify the HTTP error response.
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read status line: %v", err)
	}

	if !strings.HasPrefix(statusLine, "HTTP/1.1 500") {
		t.Fatalf("Expected HTTP 1.1 500 response, got: %s", statusLine)
	}

	// Read headers.
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read header line: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // End of headers.
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			lengthStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:"))
			contentLength, err = strconv.Atoi(lengthStr)
			if err != nil {
				t.Fatalf("Invalid Content-Length: %v", err)
			}
		}
	}

	if contentLength <= 0 {
		t.Fatal("Missing or invalid Content-Length")
	}

	// Read the response body.
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}

	var errorResp apierror.ErrorResponse
	if err := json.Unmarshal(body, &errorResp); err != nil {
		t.Fatalf("Failed to parse error JSON: %v\nBody: %s", err, string(body))
	}

	if errorResp.Code != apierror.CodePermissionDenied {
		t.Errorf("Expected error code %q, got %q", apierror.CodePermissionDenied, errorResp.Code)
	}
	expectedMsg := "Permission denied while trying to connect to Docker daemon"
	if errorResp.Message != expectedMsg {
		t.Errorf("Expected message %q, got %q", expectedMsg, errorResp.Message)
	}
}

// mockDialer implements the Dialer interface. It returns the configured error.
type mockDialer struct {
	err error
}

func (m *mockDialer) Dial(network, address string) (net.Conn, error) {
	return nil, m.err
}

// TestSystemSocketSecurity verifies the security properties of the system-wide socket
func TestSystemSocketSecurity(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "docker.sock")

	cfg := &config.SocketConfig{
		SystemSocket:         socketPath,
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	t.Run("socket_creation_race", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan error, 3)

		// Create a shared socket manager for all goroutines
		sharedSocketMgr := newTestSocketManager(socketPath)

		// Create a channel to coordinate the start of all goroutines
		start := make(chan struct{})

		// Launch concurrent attempts
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Wait for the signal to start
				<-start

				// Create a router with the shared socket manager
				r := NewRouter(zap.NewExample(), cfg, NewDefaultDialer(), sharedSocketMgr.socketManager)

				// Create a new Fx app just for lifecycle management
				app := fx.New(
					fx.Invoke(func(lc fx.Lifecycle) error {
						return r.Start(lc)
					}),
				)

				// Try to start it
				err := app.Start(context.Background())
				if err == nil {
					// If we succeeded, wait a bit then stop
					time.Sleep(50 * time.Millisecond)
					_ = app.Stop(context.Background())
				}
				results <- err
			}()
		}

		// Start all goroutines at the same time
		close(start)

		// Wait for all attempts
		wg.Wait()
		close(results)

		// Count successes vs failures
		successCount := 0
		failCount := 0
		for err := range results {
			if err == nil {
				successCount++
			} else {
				// Count any error that indicates socket contention
				errStr := err.Error()
				if strings.Contains(errStr, "socket") ||
					strings.Contains(errStr, "pid file") ||
					strings.Contains(errStr, "in use") {
					failCount++
					t.Logf("Got expected socket error: %v", err)
				} else {
					t.Logf("Unexpected error type: %v", err)
				}
			}
		}

		// We want exactly 1 success, and 2 socket-related failures
		if successCount != 1 {
			t.Errorf("Expected exactly one successful start, got %d successes", successCount)
		}
		if failCount != 2 {
			t.Errorf("Expected two socket-related failures, got %d failures", failCount)
		}
	})

	t.Run("pid_file_integrity", func(t *testing.T) {
		builder := NewTestAppBuilder(t, cfg)
		stopRouter := builder.Start(context.Background())
		defer stopRouter()

		// Verify PID file exists and contains correct PID
		pidFile := socketPath + ".pid"
		pidBytes, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("Failed to read PID file: %v", err)
		}
		expectedPID := fmt.Sprintf("%d\n", os.Getpid())
		if string(pidBytes) != expectedPID {
			t.Errorf("Incorrect PID in file. Got %q, want %q", string(pidBytes), expectedPID)
		}

		// Verify router remains functional if PID file is removed
		if err := os.Remove(pidFile); err != nil {
			t.Fatalf("Failed to remove PID file: %v", err)
		}

		// Socket should still be functional
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Errorf("Socket stopped working after PID file removal: %v", err)
		} else {
			conn.Close()
		}
	})
}

// TestUIDExtraction verifies that user identification works correctly
func TestUIDExtraction(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "docker.sock")

	cfg := &config.SocketConfig{
		SystemSocket:         socketPath,
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	builder := NewTestAppBuilder(t, cfg)
	stopRouter := builder.Start(context.Background())
	defer stopRouter()

	// Connect and verify our UID is correctly extracted
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Get our actual UID
	expectedUID := uint32(os.Getuid())

	// The router will try to connect to our user socket, which should
	// generate a specific error message containing our UID
	userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, expectedUID)

	// Send a request to trigger the router's permission check
	if _, err := conn.Write([]byte("GET /version HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}

	// Read response - it should indicate an attempt to connect to our user socket
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	response := string(buf[:n])
	if !strings.Contains(response, userSocket) {
		t.Errorf("Response doesn't reference correct user socket.\nGot: %s\nWant socket path: %s",
			response, userSocket)
	}
}

// TestSocketPermissionVerification verifies that the router correctly
// enforces access to user-specific Docker sockets
func TestSocketPermissionVerification(t *testing.T) {
	tempDir := t.TempDir()

	// Ensure cleanup of any stale files
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	// Clean up any stale files before starting
	os.RemoveAll(tempDir)
	os.MkdirAll(tempDir, 0755)

	socketPath := filepath.Join(tempDir, "docker.sock")

	cfg := &config.SocketConfig{
		SystemSocket:         socketPath,
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	builder := NewTestAppBuilder(t, cfg)
	stopRouter := builder.Start(context.Background())

	// Add a defer to ensure cleanup
	defer func() {
		stopRouter()
		os.Remove(socketPath)
		uid := uint32(os.Getuid())
		userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)
		os.Remove(userSocket)
	}()

	// Add a small delay to ensure socket is ready
	time.Sleep(100 * time.Millisecond)

	uid := uint32(os.Getuid())
	userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)

	t.Run("no_user_socket", func(t *testing.T) {
		// Ensure no user socket exists
		os.Remove(userSocket)

		// Set a timeout for the test
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Create a connection with timeout
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", socketPath)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		if _, err := conn.Write([]byte("GET /version HTTP/1.1\r\n\r\n")); err != nil {
			t.Fatalf("Failed to send request: %v", err)
		}

		// Set read deadline
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Read the HTTP response
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("Failed to read HTTP response: %v", err)
		}
		defer resp.Body.Close()

		// Read the response body
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("Failed to read response body: %v", err)
		}

		if !strings.Contains(string(body), "socket_not_found") {
			t.Errorf("Expected socket_not_found error, got: %s", string(body))
		}
	})

	t.Run("inaccessible_user_socket", func(t *testing.T) {
		// Create user socket with no permissions
		l, err := net.Listen("unix", userSocket)
		if err != nil {
			t.Fatalf("Failed to create user socket: %v", err)
		}
		defer l.Close()
		defer os.Remove(userSocket)

		// Channel to signal when we're done with the listener
		done := make(chan struct{})
		defer close(done)

		// Start a goroutine to accept connections
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					conn, err := l.Accept()
					if err != nil {
						if !strings.Contains(err.Error(), "use of closed network connection") {
							t.Errorf("Accept error: %v", err)
						}
						return
					}

					// Read the request
					buf := make([]byte, 1024)
					if _, err := conn.Read(buf); err != nil && err != io.EOF {
						t.Errorf("Failed to read request: %v", err)
						conn.Close()
						continue
					}

					// Send a proper error response with a body
					errorResp := `{"message":"Permission denied","code":"permission_denied"}`
					response := fmt.Sprintf("HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(errorResp), errorResp)
					if _, err := conn.Write([]byte(response)); err != nil {
						t.Errorf("Failed to write response: %v", err)
					}

					// Give the client time to read the response
					time.Sleep(100 * time.Millisecond)
					conn.Close()
				}
			}
		}()

		// Set socket permissions to 0000 (no access)
		if err := os.Chmod(userSocket, 0000); err != nil {
			t.Fatalf("Failed to chmod socket: %v", err)
		}

		// Create a connection with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", socketPath)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer conn.Close()

		// Send the request with a timeout
		request := "GET /version HTTP/1.1\r\nHost: unix\r\nConnection: close\r\n\r\n"
		if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("Failed to set write deadline: %v", err)
		}
		if _, err := conn.Write([]byte(request)); err != nil {
			t.Fatalf("Failed to send request: %v", err)
		}

		// Read the response with a timeout and retries
		maxRetries := 3
		var lastErr error
		var statusLine string

		// Create a buffered reader for the connection
		reader := bufio.NewReader(conn)

		for i := 0; i < maxRetries; i++ {
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				lastErr = fmt.Errorf("failed to set read deadline: %v", err)
				continue
			}

			// Read status line with a small delay between retries
			statusLine, err = reader.ReadString('\n')
			if err == nil {
				break
			}

			lastErr = err
			time.Sleep(100 * time.Millisecond)
		}

		if lastErr != nil {
			t.Fatalf("Failed to read status line after %d retries: %v", maxRetries, lastErr)
		}

		if !strings.HasPrefix(statusLine, "HTTP/1.1 403") {
			t.Errorf("Expected HTTP 403 response, got: %s", statusLine)
		}

		// Read headers with retries
		var contentLength int
		for {
			var line string
			for i := 0; i < maxRetries; i++ {
				if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					lastErr = fmt.Errorf("failed to set read deadline: %v", err)
					continue
				}

				line, err = reader.ReadString('\n')
				if err == nil {
					break
				}

				lastErr = err
				time.Sleep(100 * time.Millisecond)
			}

			if lastErr != nil {
				t.Fatalf("Failed to read header line after %d retries: %v", maxRetries, lastErr)
			}

			line = strings.TrimSpace(line)
			if line == "" {
				break // End of headers
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				lengthStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:"))
				contentLength, err = strconv.Atoi(lengthStr)
				if err != nil {
					t.Fatalf("Invalid Content-Length: %v", err)
				}
			}
		}

		if contentLength <= 0 {
			t.Fatal("Missing or invalid Content-Length")
		}

		// Read the response body with retries
		body := make([]byte, contentLength)
		for i := 0; i < maxRetries; i++ {
			if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				lastErr = fmt.Errorf("failed to set read deadline: %v", err)
				continue
			}

			_, err = io.ReadFull(reader, body)
			if err == nil {
				break
			}

			lastErr = err
			time.Sleep(100 * time.Millisecond)
		}

		if lastErr != nil {
			t.Fatalf("Failed to read body after %d retries: %v", maxRetries, lastErr)
		}

		var errorResp apierror.ErrorResponse
		if err := json.Unmarshal(body, &errorResp); err != nil {
			t.Fatalf("Failed to parse error JSON: %v\nBody: %s", err, string(body))
		}

		if errorResp.Code != apierror.CodePermissionDenied {
			t.Errorf("Expected permission_denied error, got: %s", errorResp.Code)
		}
	})
}
