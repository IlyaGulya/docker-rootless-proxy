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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
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

		router := NewRouter(logger, cfg)

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
	withTestLogger(t, func(logger *zap.Logger) {
		tempDir := t.TempDir()

		cfg := &config.SocketConfig{
			SystemSocket:         filepath.Join(tempDir, "docker.sock"),
			RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
		}

		t.Run("normal_lifecycle", func(t *testing.T) {
			app := fxtest.New(t,
				fx.Provide(
					func() *zap.Logger { return logger },
					func() *config.SocketConfig { return cfg },
					NewRouter,
				),
				fx.Invoke(func(lc fx.Lifecycle, r *Router) error {
					return r.Start(lc)
				}),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			if err := app.Start(ctx); err != nil {
				t.Fatalf("Failed to start app: %v", err)
			}

			// Verify socket and PID file exist
			info, err := os.Stat(cfg.SystemSocket)
			if err != nil {
				t.Fatalf("Socket not created: %v", err)
			}
			if info.Mode()&os.ModePerm != 0666 {
				t.Errorf("Expected socket permissions 0666, got %v", info.Mode()&os.ModePerm)
			}

			// Verify PID file
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

			if err := app.Stop(ctx); err != nil {
				t.Fatalf("Failed to stop app: %v", err)
			}

			// Verify cleanup
			if _, err := os.Stat(cfg.SystemSocket); !os.IsNotExist(err) {
				t.Error("Socket file should not exist after shutdown")
			}
			if _, err := os.Stat(cfg.SystemSocket + ".pid"); !os.IsNotExist(err) {
				t.Error("PID file should not exist after shutdown")
			}
		})

		t.Run("failed_socket_creation", func(t *testing.T) {
			// Create a directory with the same name to force a socket creation failure
			if err := os.Mkdir(cfg.SystemSocket, 0755); err != nil {
				t.Fatal(err)
			}

			app := fxtest.New(t,
				fx.Provide(
					func() *zap.Logger { return logger },
					func() *config.SocketConfig { return cfg },
					NewRouter,
				),
				fx.Invoke(func(lc fx.Lifecycle, r *Router) {
					err := r.Start(lc)
					if err == nil {
						t.Error("Expected error on start, got nil")
					}
				}),
			)

			app.RequireStart()
			app.RequireStop()
		})
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

	// Create a mock Docker user socket
	userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)
	userListener, err := net.Listen("unix", userSocket)
	if err != nil {
		t.Fatalf("Failed to create user socket: %v", err)
	}
	defer userListener.Close()

	// Basic echo server
	go func() {
		for {
			conn, err := userListener.Accept()
			if err != nil {
				return
			}
			go echo(conn)
		}
	}()

	// Start the router
	stopRouter := startTestRouter(t, cfg)
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

func startTestRouter(t *testing.T, cfg *config.SocketConfig) (stopFn func()) {
	logger, cleanup := threadSafeTestLogger(t)

	app := fxtest.New(t,
		fx.Provide(
			func() *zap.Logger { return logger },
			func() *config.SocketConfig { return cfg },
			NewRouter,
		),
		fx.Invoke(func(lc fx.Lifecycle, r *Router) error {
			return r.Start(lc)
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

	if err := app.Start(ctx); err != nil {
		cleanup()
		cancel()
		t.Fatalf("Failed to start Fx app: %v", err)
	}

	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer func() {
			stopCancel()
			cancel()
			cleanup()
		}()

		if err := app.Stop(stopCtx); err != nil {
			t.Errorf("Failed to stop Fx app: %v", err)
		}
	}
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

	// Start the router
	stopRouter := startTestRouter(t, cfg)
	defer stopRouter()

	// -- Test scenario: "missing_socket" => user socket does not exist.
	dialer := net.Dialer{Timeout: 1 * time.Second}

	conn, err := dialer.Dial("unix", cfg.SystemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	defer conn.Close()

	// Set a read deadline so we don't hang forever
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set deadline: %v", err)
	}

	// Attempt any write (router doesn't care about data). The router will try to connect
	// to the user socket and fail with "no such file or directory." We expect an HTTP error response.
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	reader := bufio.NewReaderSize(conn, 4096)

	// Read status line
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read status line: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 500") {
		t.Errorf("Expected HTTP/1.1 500 response, got: %s", statusLine)
	}

	// Read headers
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read header line: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // end of headers
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
		t.Fatalf("Invalid content length: %d", contentLength)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}

	var errorResp apierror.ErrorResponse
	if err := json.Unmarshal(body, &errorResp); err != nil {
		t.Fatalf("Failed to parse error JSON: %v\nBody: %s", err, string(body))
	}

	if errorResp.Code != apierror.CodeSocketNotFound {
		t.Errorf("Expected error code %q, got %q", apierror.CodeSocketNotFound, errorResp.Code)
	}
	expectedMsg := fmt.Sprintf("Cannot connect to Docker daemon at %s", fmt.Sprintf(cfg.RootlessSocketFormat, uid))
	if errorResp.Message != expectedMsg {
		t.Errorf("Expected message %q, got %q", expectedMsg, errorResp.Message)
	}
}

// TestRouterErrorResponsePermissionDenied checks that if the router tries to connect
// to a user socket but lacks permissions, we get a 500 JSON error with CodePermissionDenied.
// Many systems return EACCES. If running as root, skip this test because root can usually connect.
func TestRouterErrorResponsePermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("Skipping permission_denied test because running as root will almost never EACCES.")
	}

	tempDir := t.TempDir()
	uid := os.Getuid()

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	// Create the “user socket” and lock it down so the current user can’t connect.
	userSocket := fmt.Sprintf(cfg.RootlessSocketFormat, uid)

	// Make sure the directory exists
	if err := os.MkdirAll(filepath.Dir(userSocket), 0755); err != nil {
		t.Fatalf("Failed to create directory for user socket: %v", err)
	}

	// Create the socket by listening on it:
	l, err := net.Listen("unix", userSocket)
	if err != nil {
		t.Fatalf("Failed to create test user socket: %v", err)
	}
	// We keep the listener open so the socket file definitely exists.
	// In a real "permission denied" scenario, the file's owner or mode will block us.

	// Attempt to set ownership to root (assuming test user is not root).
	// Then restrict mode to 0700 so only root can open.
	// On most Linux systems, that yields EACCES for a non-root dialer.
	if err := os.Chown(userSocket, 0, 0); err != nil && !os.IsPermission(err) {
		l.Close()
		t.Fatalf("Failed to chown to root: %v", err)
	}
	if err := os.Chmod(userSocket, 0700); err != nil {
		l.Close()
		t.Fatalf("Failed to chmod to 0700: %v", err)
	}

	// Clean up after the test
	t.Cleanup(func() {
		l.Close()
		os.Remove(userSocket)
	})

	// Start the router
	stopRouter := startTestRouter(t, cfg)
	defer stopRouter()

	// Dial the router. The router should try to dial userSocket and (hopefully) fail with permission error.
	dialer := net.Dialer{Timeout: 1 * time.Second}
	conn, err := dialer.Dial("unix", cfg.SystemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	defer conn.Close()

	// Set a read deadline so we don't hang forever
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set deadline: %v", err)
	}

	// Write something to trigger the router’s handleConnection
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Now read the HTTP error response
	reader := bufio.NewReaderSize(conn, 4096)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read status line: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 500") {
		t.Errorf("Expected HTTP/1.1 500 response, got: %s", statusLine)
	}

	// Read headers
	var contentLength int
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("Failed to read header line: %v", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break // end of headers
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
		t.Fatalf("Invalid content length: %d", contentLength)
	}

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
