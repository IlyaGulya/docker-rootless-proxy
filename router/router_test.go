package router

import (
	"context"
	"docker-socket-router/config"
	"fmt"
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
	"go.uber.org/zap/zaptest"
)

// TestRouterGetUserUid verifies that the router can correctly retrieve the user's UID.
func TestRouterGetUserUid(t *testing.T) {
	logger := zaptest.NewLogger(t)
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

	t.Run("invalid_connection", func(t *testing.T) {
		invalidConn := &net.UnixConn{}
		_, err := router.getUserUid(invalidConn)
		if err == nil {
			t.Error("Expected error for invalid connection, got nil")
		}
	})
}

// TestRouterLifecycle verifies that the router starts/stops cleanly
// (creating and removing the system socket and associated PID file).
func TestRouterLifecycle(t *testing.T) {
	logger := zaptest.NewLogger(t)
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
}

// TestIntegration - Example of an integration test verifying that the router
// correctly forwards data between the system socket and a mock rootless socket.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	logger := zaptest.NewLogger(t)
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

	// Basic echo server for user socket
	go func() {
		for {
			conn, err := userListener.Accept()
			if err != nil {
				return
			}
			go echo(conn)
		}
	}()

	// Start the router using Fx
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := app.Start(ctx); err != nil {
		t.Fatalf("Failed to start app: %v", err)
	}

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

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("Failed to stop app: %v", err)
	}
}

func testConnection(t *testing.T, socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	defer conn.Close()

	testData := []byte("test message")
	if _, err := conn.Write(testData); err != nil {
		return fmt.Errorf("failed to write: %v", err)
	}

	buf := make([]byte, len(testData))
	if _, err := conn.Read(buf); err != nil {
		return fmt.Errorf("failed to read: %v", err)
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
