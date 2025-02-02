package router

import (
	"docker-socket-router/config"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestSocketManagerAcquisition verifies that socket_manager can acquire/release
// the system socket under various conditions, including stale PID files and
// existing active sockets.
func TestSocketManagerAcquisition(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.SocketConfig{
		SystemSocket:         filepath.Join(tempDir, "docker.sock"),
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"),
	}

	tests := []struct {
		name    string
		setup   func() error
		cleanup func()
		wantErr bool
	}{
		{
			name:  "clean_start",
			setup: func() error { return nil },
			cleanup: func() {
				// nothing extra
			},
			wantErr: false,
		},
		{
			name: "active_socket",
			setup: func() error {
				// Simulate something else currently listening
				l, err := net.Listen("unix", cfg.SystemSocket)
				if err != nil {
					return err
				}
				// Keep it open in the background
				go func() {
					for {
						conn, _ := l.Accept()
						if conn != nil {
							conn.Close()
						}
					}
				}()
				return nil
			},
			cleanup: func() {
				// nothing extra
			},
			wantErr: true,
		},
		{
			name: "stale_socket_no_pid",
			setup: func() error {
				// Create a stale socket file but no .pid
				return os.WriteFile(cfg.SystemSocket, []byte("stale data"), 0666)
			},
			cleanup: func() {
				// nothing extra
			},
			wantErr: true,
		},
		{
			name: "invalid_permissions",
			setup: func() error {
				if err := os.WriteFile(cfg.SystemSocket, []byte(""), 0666); err != nil {
					return err
				}
				// Lock down permissions so we can’t do anything with the file
				return os.Chmod(cfg.SystemSocket, 0000)
			},
			cleanup: func() {
				// Restore permissions so the test can clean up the file
				os.Chmod(cfg.SystemSocket, 0666)
			},
			wantErr: true,
		},
		{
			name: "existing_active_process",
			setup: func() error {
				pidFile := cfg.SystemSocket + ".pid"
				// Write our current PID to the pidFile
				return os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
			},
			cleanup: func() {
				// nothing extra
			},
			wantErr: true,
		},
		{
			name: "stale_process_files",
			setup: func() error {
				// Write a bogus large PID + stale socket file
				pidFile := cfg.SystemSocket + ".pid"
				if err := os.WriteFile(cfg.SystemSocket, []byte("stale"), 0666); err != nil {
					return err
				}
				return os.WriteFile(pidFile, []byte("999999999\n"), 0644)
			},
			cleanup: func() {
				// nothing extra
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure a clean slate each iteration
			_ = os.Remove(cfg.SystemSocket)
			_ = os.Remove(cfg.SystemSocket + ".pid")

			if err := tt.setup(); err != nil {
				t.Fatalf("setup failed: %v", err)
			}
			defer tt.cleanup()

			sm := NewSocketManager(cfg.SystemSocket)
			acquired, err := sm.acquireSocket()

			if (err != nil) != tt.wantErr {
				t.Errorf("acquireSocket() error = %v, wantErr %v", err, tt.wantErr)
			}

			// If we expect no error, we typically acquired the socket
			if !tt.wantErr && !acquired {
				t.Error("Expected to acquire socket but did not")
			}

			// Release the socket to test normal shutdown/cleanup
			releaseErr := sm.releaseSocket()
			if releaseErr != nil {
				t.Errorf("releaseSocket() error = %v", releaseErr)
			}

			// If we successfully acquired it, verify the files are gone
			if !tt.wantErr && acquired {
				if _, statErr := os.Stat(cfg.SystemSocket); !os.IsNotExist(statErr) {
					t.Error("Socket file should be removed after release")
				}
				if _, statErr := os.Stat(cfg.SystemSocket + ".pid"); !os.IsNotExist(statErr) {
					t.Error("PID file should be removed after release")
				}
			}
		})
	}
}
