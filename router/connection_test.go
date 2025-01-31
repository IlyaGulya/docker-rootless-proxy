package router

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

type mockConn struct {
	net.Conn
	readData  []byte
	writeData []byte
	closed    bool
}

func (m *mockConn) Read(p []byte) (n int, err error) {
	if len(m.readData) == 0 {
		return 0, io.EOF
	}
	n = copy(p, m.readData)
	m.readData = m.readData[n:]
	return n, nil
}

func (m *mockConn) Write(p []byte) (n int, err error) {
	m.writeData = append(m.writeData, p...)
	return len(p), nil
}

func (m *mockConn) Close() error {
	m.closed = true
	return nil
}

func TestConnection(t *testing.T) {
	logger := zaptest.NewLogger(t)
	clientData := []byte("client message")
	dockerData := []byte("docker response")

	clientConn := &mockConn{readData: clientData}
	dockerConn := &mockConn{readData: dockerData}

	conn := NewConnection(logger, clientConn, dockerConn)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	conn.Handle(ctx)

	if !clientConn.closed {
		t.Error("client connection not closed")
	}
	if !dockerConn.closed {
		t.Error("docker connection not closed")
	}

	if string(dockerConn.writeData) != string(clientData) {
		t.Errorf("expected docker to receive %q, got %q", clientData, dockerConn.writeData)
	}
}

func TestConnectionContextCancellation(t *testing.T) {
	logger := zaptest.NewLogger(t)
	clientConn := &mockConn{readData: make([]byte, 1024)}
	dockerConn := &mockConn{readData: make([]byte, 1024)}

	conn := NewConnection(logger, clientConn, dockerConn)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	conn.Handle(ctx)

	if !clientConn.closed {
		t.Error("client connection should be closed after context cancellation")
	}
	if !dockerConn.closed {
		t.Error("docker connection should be closed after context cancellation")
	}
}
