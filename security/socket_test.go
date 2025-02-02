package security

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestCanAccessSocket(t *testing.T) {
	// Skip if not running as root since we need to be able to change ownership
	if os.Geteuid() != 0 {
		t.Skip("Test requires root privileges to run")
	}

	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "test.sock")

	// Create a test Unix domain socket
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)

	currentUID := uint32(os.Getuid())

	// Get a non-root user for testing
	nonRootUser, err := user.Lookup("nobody")
	if err != nil {
		t.Fatalf("Failed to lookup nobody user: %v", err)
	}
	nonRootUID, err := strconv.ParseUint(nonRootUser.Uid, 10, 32)
	if err != nil {
		t.Fatalf("Failed to parse nobody UID: %v", err)
	}
	nonRootGID, err := strconv.ParseUint(nonRootUser.Gid, 10, 32)
	if err != nil {
		t.Fatalf("Failed to parse nobody GID: %v", err)
	}

	// Get another non-root user for group testing (try "daemon" first, fallback to "nobody")
	groupTestUser, err := user.Lookup("daemon")
	if err != nil {
		groupTestUser = nonRootUser // Fallback to nobody if daemon doesn't exist
	}
	groupTestUID, err := strconv.ParseUint(groupTestUser.Uid, 10, 32)
	if err != nil {
		t.Fatalf("Failed to parse group test user UID: %v", err)
	}

	tests := []struct {
		name       string
		uid        uint32
		mode       os.FileMode
		chown      *struct{ uid, gid int }
		want       bool
		wantErrStr string
	}{
		{
			name: "full_permissions",
			uid:  currentUID,
			mode: 0666,
			want: true,
		},
		{
			name: "no_permissions",
			uid:  uint32(nonRootUID),
			mode: 0600, // Only owner can access
			want: false,
		},
		{
			name: "read_only",
			uid:  uint32(nonRootUID),
			mode: 0444,
			want: false, // We require both read and write
		},
		{
			name: "write_only",
			uid:  uint32(nonRootUID),
			mode: 0222,
			want: false, // We require both read and write
		},
		{
			name: "group_access",
			uid:  uint32(groupTestUID),
			mode: 0660,
			chown: &struct{ uid, gid int }{
				uid: int(currentUID),
				gid: int(nonRootGID), // Use nobody's group
			},
			want: false, // Should be false as we're not actually in the group
		},
		{
			name: "different_owner_no_access",
			uid:  uint32(nonRootUID),
			mode: 0600,
			chown: &struct{ uid, gid int }{
				uid: int(currentUID),
				gid: int(currentUID),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Chmod(socketPath, tt.mode); err != nil {
				t.Fatalf("Failed to chmod: %v", err)
			}

			if tt.chown != nil {
				if err := os.Chown(socketPath, tt.chown.uid, tt.chown.gid); err != nil {
					t.Fatalf("Failed to chown: %v", err)
				}
			} else {
				// Reset ownership to current user if not specified
				if err := os.Chown(socketPath, int(currentUID), int(currentUID)); err != nil {
					t.Fatalf("Failed to reset ownership: %v", err)
				}
			}

			got, err := CanAccessSocket(socketPath, tt.uid)
			if err != nil {
				if tt.wantErrStr == "" {
					t.Errorf("CanAccessSocket() unexpected error: %v", err)
				} else if errStr := err.Error(); errStr != tt.wantErrStr {
					t.Errorf("CanAccessSocket() error = %v, want %v", errStr, tt.wantErrStr)
				}
				return
			}
			if got != tt.want {
				t.Errorf("CanAccessSocket() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanAccessSocketErrors(t *testing.T) {
	tempDir := t.TempDir()

	// Create a test socket that we'll use for the invalid UID test
	testSocket := filepath.Join(tempDir, "test.sock")
	listener, err := net.Listen("unix", testSocket)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(testSocket)

	tests := []struct {
		name    string
		setup   func(string) string
		uid     uint32
		wantErr bool
	}{
		{
			name: "non_existent_socket",
			setup: func(dir string) string {
				return filepath.Join(dir, "nonexistent.sock")
			},
			uid:     1000,
			wantErr: false, // Should return false, not error
		},
		{
			name: "regular_file_not_socket",
			setup: func(dir string) string {
				path := filepath.Join(dir, "regular.file")
				os.WriteFile(path, []byte("test"), 0600)
				return path
			},
			uid:     1000,
			wantErr: true,
		},
		{
			name: "invalid_uid",
			setup: func(dir string) string {
				return testSocket // Use the pre-created socket
			},
			uid:     0xFFFFFFFF, // Invalid UID that should not exist
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := tt.setup(tempDir)
			_, err := CanAccessSocket(socketPath, tt.uid)
			if (err != nil) != tt.wantErr {
				t.Errorf("CanAccessSocket() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
