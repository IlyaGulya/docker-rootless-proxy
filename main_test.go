package main

import (
	"context"
	"fmt"
	"go.uber.org/zap/zaptest"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
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

	err = checkExistingSocket(socketPath)
	if err == nil {
		t.Error("Expected error for active socket, got nil")
	}
	listener.Close()

	// Test stale socket
	if err := os.WriteFile(socketPath, []byte("stale"), 0666); err != nil {
		t.Fatalf("Failed to create stale socket file: %v", err)
	}

	err = checkExistingSocket(socketPath)
	if err == nil {
		t.Error("Expected error for stale socket")
	} else if !strings.Contains(err.Error(), "appears to be stale") {
		t.Errorf("Expected stale socket error, got: %v", err)
	}
}

func TestConnection(t *testing.T) {
	logger := zaptest.NewLogger(t)
	tempDir := t.TempDir()

	// Set up the mock docker server
	dockerSocket := filepath.Join(tempDir, "docker.sock")
	dockerListener, err := net.Listen("unix", dockerSocket)
	if err != nil {
		t.Fatalf("Failed to create mock Docker socket: %v", err)
	}
	defer dockerListener.Close()

	// Channel to coordinate server shutdown
	serverDone := make(chan struct{})
	messageReceived := make(chan struct{})

	// Set up server handler
	go func() {
		defer close(serverDone)
		conn, err := dockerListener.Accept()
		if err != nil {
			t.Errorf("docker accept error: %v", err)
			return
		}
		defer conn.Close()

		// Echo server with synchronization
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				t.Errorf("docker read error: %v", err)
			}
			return
		}
		close(messageReceived)

		_, err = conn.Write(buf[:n])
		if err != nil && err != io.EOF {
			t.Errorf("docker write error: %v", err)
		}
	}()

	// Connect to mock docker
	dockerConn, err := net.Dial("unix", dockerSocket)
	if err != nil {
		t.Fatalf("Failed to connect to mock Docker: %v", err)
	}

	// Create a pipe for the client side
	clientReader, clientWriter := net.Pipe()

	// Create connection handler
	conn := &Connection{
		logger:     logger,
		clientConn: clientReader,
		dockerConn: dockerConn,
	}

	// Start connection handler
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		conn.handleConnection(ctx)
	}()

	// Write test data
	testData := []byte("test message")
	_, err = clientWriter.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write test data: %v", err)
	}

	// Wait for message to be received
	select {
	case <-messageReceived:
		// Message was received by mock server
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for message to be received")
	}

	// Clean shutdown
	clientWriter.Close()

	// Wait for handler to finish
	select {
	case <-handlerDone:
		// Handler completed
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for handler to complete")
	}

	// Wait for server to finish
	select {
	case <-serverDone:
		// Server completed
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for server to complete")
	}
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	uid := os.Getuid()
	userSocket := filepath.Join(tempDir, fmt.Sprintf("user_%d.sock", uid))
	systemSocket := filepath.Join(tempDir, "docker.sock")

	// Start mock user Docker daemon
	userListener, err := net.Listen("unix", userSocket)
	if err != nil {
		t.Fatalf("Failed to create user socket: %v", err)
	}
	defer userListener.Close()

	// Channel to track server readiness
	serverReady := make(chan struct{})
	serverDone := make(chan struct{})

	// Create context for the test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Echo server
	go func() {
		defer close(serverDone)
		close(serverReady) // Signal server is ready to accept
		for {
			conn, err := userListener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return // Normal shutdown
				default:
					t.Errorf("Accept error: %v", err)
					continue
				}
			}
			go echo(conn)
		}
	}()

	// Wait for echo server to be ready
	<-serverReady

	// Create test configuration with socket format
	config := SocketConfig{
		SystemSocket:         systemSocket,
		RootlessSocketFormat: filepath.Join(tempDir, "user_%d.sock"), // Format string for user socket
	}

	mainErr := make(chan error, 1)
	logger := zaptest.NewLogger(t)

	go func() {
		mainErr <- startRouter(ctx, config, logger)
	}()

	// Wait for system socket to be available
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(systemSocket); err == nil {
			time.Sleep(50 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Connect and send data
	conn, err := net.Dial("unix", systemSocket)
	if err != nil {
		t.Fatalf("Failed to connect to router: %v", err)
	}
	defer conn.Close()

	// Write test data
	testData := []byte("test message")
	_, err = conn.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write: %v", err)
	}

	// Read response
	buf := make([]byte, len(testData))
	readChan := make(chan []byte)
	readErrChan := make(chan error)

	go func() {
		n, err := conn.Read(buf)
		if err != nil {
			readErrChan <- err
			return
		}
		readChan <- buf[:n]
	}()

	// Wait for response
	select {
	case data := <-readChan:
		if string(data) != string(testData) {
			t.Errorf("Expected %q, got %q", testData, data)
		}
	case err := <-readErrChan:
		t.Fatalf("Failed to read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for response")
	}

	// Clean shutdown
	cancel()

	select {
	case err := <-mainErr:
		if err != nil {
			t.Fatalf("main exited with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for clean shutdown")
	}
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

func TestGetUserId(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Failed to create test socket: %v", err)
	}
	defer listener.Close()

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
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

	<-acceptDone
}
