package security

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// CanAccessSocket checks if a user with the given UID has read/write access to the socket
// by attempting to run the test command as that user.
// Returns (false, nil) if the socket doesn't exist.
func CanAccessSocket(socketPath string, uid uint32) (bool, error) {
	info, err := os.Stat(socketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // Socket doesn't exist is a valid case
		}
		return false, fmt.Errorf("failed to stat socket: %w", err)
	}

	// If the file exists but is not a socket, that's an error
	if info.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("path %s is not a socket", socketPath)
	}

	// Validate that the user exists
	u, err := user.LookupId(fmt.Sprint(uid))
	if err != nil {
		return false, fmt.Errorf("invalid user ID %d: %w", uid, err)
	}

	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return false, fmt.Errorf("failed to parse GID: %w", err)
	}

	// Get supplementary groups
	groups, err := u.GroupIds()
	if err != nil {
		return false, fmt.Errorf("failed to get supplementary groups: %w", err)
	}

	// Convert group IDs to uint32
	groupIDs := make([]uint32, 0, len(groups))
	for _, gidStr := range groups {
		gid, err := strconv.ParseUint(gidStr, 10, 32)
		if err != nil {
			continue // Skip invalid group IDs
		}
		groupIDs = append(groupIDs, uint32(gid))
	}

	// Create test command for read permission
	readCmd := exec.Command("test", "-r", socketPath)
	// Create test command for write permission
	writeCmd := exec.Command("test", "-w", socketPath)

	// Set up the commands to run as the specified user with their primary group and supplementary groups
	cred := &syscall.Credential{
		Uid:    uid,
		Gid:    uint32(gid),
		Groups: groupIDs,
	}

	readCmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: cred,
	}
	writeCmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: cred,
	}

	// Run the commands
	readErr := readCmd.Run()
	writeErr := writeCmd.Run()

	// Both commands need to succeed for read/write access
	return readErr == nil && writeErr == nil, nil
}
