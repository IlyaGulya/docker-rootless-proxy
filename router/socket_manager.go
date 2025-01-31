package router

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type socketManager struct {
	socketPath string
	pidFile    string
}

func newSocketManager(socketPath string) *socketManager {
	return &socketManager{
		socketPath: socketPath,
		pidFile:    socketPath + ".pid",
	}
}

// acquireSocket attempts to create or take ownership of the socket
// Returns true if successful, false if socket is owned by another process
func (sm *socketManager) acquireSocket() (bool, error) {
	// 1) Does the pid file already exist?
	pid, err := sm.readPIDFile()
	switch {
	case err == nil:
		// We read a PID successfully, so we check if that process is running
		if sm.isProcessRunning(pid) {
			// Another process definitely owns the socket
			return false, fmt.Errorf("socket %s is in use by process %d", sm.socketPath, pid)
		}
		// Otherwise, it's stale: remove both .pid and .sock
		if removeErr := os.Remove(sm.pidFile); removeErr != nil && !os.IsNotExist(removeErr) {
			return false, fmt.Errorf("failed to remove stale pid file: %v", removeErr)
		}
		if removeErr := os.Remove(sm.socketPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return false, fmt.Errorf("failed to remove stale socket: %v", removeErr)
		}

	case os.IsNotExist(err):
		// 2) No PID file => If the socket file exists, fail. We never clean up a socket we didn't create
		if _, sockErr := os.Stat(sm.socketPath); sockErr == nil {
			return false, fmt.Errorf(
				"socket file %s already exists but no pid file found: refusing to remove it",
				sm.socketPath,
			)
		}
		// Otherwise, if the socket file doesn't exist, we are free to create it

	default:
		// Some other error reading the pid file
		return false, fmt.Errorf("failed to read pid file %s: %v", sm.pidFile, err)
	}

	// 3) Now we create a PID file with O_EXCL. If it fails with IsExist, it means a race or another instance started up
	f, err := os.OpenFile(sm.pidFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			return false, fmt.Errorf("pid file %s was just created by another process", sm.pidFile)
		}
		return false, fmt.Errorf("failed to create pid file: %v", err)
	}
	defer f.Close()

	// 4) Write our PID
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		// Cleanup
		os.Remove(sm.pidFile)
		return false, fmt.Errorf("failed to write pid file: %v", err)
	}

	// Successfully acquired
	return true, nil
}

// releaseSocket removes the socket only if we still own it
func (sm *socketManager) releaseSocket() error {
	// Verify we own the socket by checking the PID file
	pid, err := sm.readPIDFile()
	if err != nil {
		// If no pid file or can't parse, we do nothing
		return nil
	}
	if pid != os.Getpid() {
		return nil // Not ours
	}

	// Remove pid file first
	if err := os.Remove(sm.pidFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove pid file: %v", err)
	}

	// Then remove socket
	if err := os.Remove(sm.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove socket: %v", err)
	}
	return nil
}

// readPIDFile reads & parses the pid file into an int, trimming spaces/newlines.
func (sm *socketManager) readPIDFile() (int, error) {
	data, err := os.ReadFile(sm.pidFile)
	if err != nil {
		return 0, err
	}
	pidStr := strings.TrimSpace(string(data))
	return strconv.Atoi(pidStr)
}

// isProcessRunning returns true if a process with the given PID is still running.
func (sm *socketManager) isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 does not send an actual signal, but it will
	// return an error if the process does not exist.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
