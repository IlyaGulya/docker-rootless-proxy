package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckExistingSocket(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "test.sock")

	// Test non-existent socket
	err := checkExistingSocket(socketPath)
	if err != nil {
		t.Errorf("Expected no error for non-existent socket, got: %v", err)
	}

	// Test active socket
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	defer listener.Close()

	err = checkExistingSocket(socketPath)
	if err == nil {
		t.Error("Expected error for active socket, got nil")
	}

	// Test stale socket
	listener.Close()
	err = checkExistingSocket(socketPath)
	if err == nil {
		t.Error("Expected error for stale socket, got nil")
	}
}

func TestLogger(t *testing.T) {
	logger, err := NewLogger()
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Test info logging
	logger.Info("test info message")

	// Test error logging
	logger.Error("test error message")
}

func TestConnection(t *testing.T) {
	logger, _ := NewLogger()
	tempDir := t.TempDir()

	// Create mock Docker socket
	dockerSocket := filepath.Join(tempDir, "docker.sock")
	dockerListener, err := net.Listen("unix", dockerSocket)
	if err != nil {
		t.Fatalf("Failed to create mock Docker socket: %v", err)
	}
	defer dockerListener.Close()

	// Create client socket
	clientSocket := filepath.Join(tempDir, "client.sock")
	clientListener, err := net.Listen("unix", clientSocket)
	if err != nil {
		t.Fatalf("Failed to create client socket: %v", err)
	}
	defer clientListener.Close()

	// Setup test data
	testData := []byte("test message")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start mock Docker server
	go func() {
		conn, err := dockerListener.Accept()
		if err != nil {
			t.Errorf("Docker accept error: %v", err)
			return
		}
		defer conn.Close()

		buf := make([]byte, len(testData))
		n, err := conn.Read(buf)
		if err != nil {
			t.Errorf("Docker read error: %v", err)
			return
		}

		// Echo back
		conn.Write(buf[:n])
	}()

	// Connect client
	clientConn, err := net.Dial("unix", clientSocket)
	if err != nil {
		t.Fatalf("Failed to connect client: %v", err)
	}

	// Connect to mock Docker
	dockerConn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		t.Fatalf("Failed to connect to mock Docker: %v", err)
	}

	conn := &Connection{
		logger:     logger,
		clientConn: clientConn,
		dockerConn: dockerConn,
	}

	go conn.handleConnection(ctx)

	// Send test data
	_, err = clientConn.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}

	// Read response
	buf := make([]byte, len(testData))
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if string(buf[:n]) != string(testData) {
		t.Errorf("Expected %s, got %s", testData, buf[:n])
	}
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	systemSocket := filepath.Join(tempDir, "docker.sock")
	userSocket := filepath.Join(tempDir, "user.sock")

	// Start mock user Docker daemon
	userListener, err := net.Listen("unix", userSocket)
	if err != nil {
		t.Fatalf("Failed to create user socket: %v", err)
	}
	defer userListener.Close()

	// Set up echo server for mock Docker daemon
	go func() {
		for {
			conn, err := userListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				c.Write(buf[:n])
			}(conn)
		}
	}()

	// Override socket paths for testing
	os.Setenv("SYSTEM_SOCKET", systemSocket)
	os.Setenv("USER_SOCKET_FORMAT", userSocket)

	// Start router in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		main()
	}()

	// Wait for router to start
	time.Sleep(time.Second)

	// Test connection
	conn, err := net.Dial("unix", systemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	defer conn.Close()

	testMsg := []byte("test message")
	_, err = conn.Write(testMsg)
	if err != nil {
		t.Fatalf("Failed to write test message: %v", err)
	}

	buf := make([]byte, len(testMsg))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	if string(buf[:n]) != string(testMsg) {
		t.Errorf("Expected %s, got %s", testMsg, buf[:n])
	}
}

func TestGetUserUid(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			t.Errorf("Accept error: %v", err)
			return
		}
		defer conn.Close()
	}()

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to connect to test socket: %v", err)
	}
	defer conn.Close()

	uid, err := getUserUid(conn)
	if err != nil {
		t.Fatalf("Failed to get UID: %v", err)
	}

	if uid != uint32(os.Getuid()) {
		t.Errorf("Expected UID %d, got %d", os.Getuid(), uid)
	}
}
