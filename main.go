package main

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"log"
	"log/syslog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	systemSocket         = "/var/run/docker.sock"
	rootlessSocketFormat = "/run/user/%d/docker.sock"
)

type Logger struct {
	syslog *syslog.Writer
}

func NewLogger() (*Logger, error) {
	syslogWriter, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "docker-socket-router")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize syslog: %v", err)
	}
	return &Logger{syslog: syslogWriter}, nil
}

func (l *Logger) Info(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if err := l.syslog.Info(msg); err != nil {
		// If syslog fails, ensure we at least have the message in standard log
		log.Printf("ERROR: Failed to write to syslog: %v", err)
	}
	log.Printf("INFO: %s", msg)
}

func (l *Logger) Error(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if err := l.syslog.Err(msg); err != nil {
		// If syslog fails, ensure we at least have the message in standard log
		log.Printf("ERROR: Failed to write to syslog: %v", err)
	}
	log.Printf("ERROR: %s", msg)
}

type Connection struct {
	logger        *Logger
	clientConn    net.Conn
	dockerConn    net.Conn
	bytesToDocker int64
	fromDocker    int64
}

func (c *Connection) copyData(dst net.Conn, src net.Conn, bytes *int64) error {
	written, err := io.Copy(dst, src)
	*bytes += written
	return err
}

func (c *Connection) handleConnection(ctx context.Context) {
	defer c.clientConn.Close()
	defer c.dockerConn.Close()

	startTime := time.Now()
	errChan := make(chan error, 2)

	go func() {
		errChan <- c.copyData(c.dockerConn, c.clientConn, &c.bytesToDocker)
	}()

	go func() {
		errChan <- c.copyData(c.clientConn, c.dockerConn, &c.fromDocker)
	}()

	// Wait for either context cancellation or copy completion
	select {
	case <-ctx.Done():
		c.logger.Info("Connection terminated by context")
	case err := <-errChan:
		if err != nil && err != io.EOF {
			c.logger.Error("Data transfer error: %v", err)
		}
	}

	duration := time.Since(startTime)
	c.logger.Info("Connection closed. Duration: %v, Bytes to Docker: %d, Bytes from Docker: %d",
		duration, c.bytesToDocker, c.fromDocker)
}

func getUserUid(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("connection is not a Unix socket")
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("failed to get syscall conn: %v", err)
	}

	var cred *unix.Ucred
	var credErr error

	err = raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, fmt.Errorf("control error: %v", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("getsockopt error: %v", credErr)
	}

	return cred.Uid, nil
}

func checkExistingSocket(path string) error {
	if _, err := os.Stat(path); err == nil {
		// Try to connect to check if it's a functioning socket
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return fmt.Errorf("socket %s already exists and is active", path)
		}
		// If we can't connect, socket might be stale but still exists
		return fmt.Errorf("socket %s exists but appears to be stale. Please check if another instance is running and remove the socket file if it's not in use", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking socket %s: %v", path, err)
	}
	return nil
}

func main() {
	logger, err := NewLogger()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Check for existing socket before proceeding
	if err := checkExistingSocket(systemSocket); err != nil {
		logger.Error("Startup failed: %v", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Info("Received shutdown signal")
		cancel()
	}()

	// Remove existing socket if present
	os.Remove(systemSocket)

	listener, err := net.Listen("unix", systemSocket)
	if err != nil {
		logger.Error("Failed to create listener: %v", err)
		return
	}
	defer listener.Close()

	// Set socket permissions
	if err := os.Chmod(systemSocket, 0666); err != nil {
		logger.Error("Failed to set socket permissions: %v", err)
		return
	}

	logger.Info("Listening on %s", systemSocket)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // Normal shutdown
			}
			logger.Error("Accept error: %v", err)
			continue
		}

		go func() {
			uid, err := getUserUid(clientConn)
			if err != nil {
				logger.Error("Failed to get user credentials: %v", err)
				clientConn.Close()
				return
			}

			userSocket := fmt.Sprintf(rootlessSocketFormat, uid)
			logger.Info("Connection from uid=%d, routing to %s", uid, userSocket)

			dockerConn, err := net.Dial("unix", userSocket)
			if err != nil {
				logger.Error("Failed to connect to user socket %s: %v", userSocket, err)
				clientConn.Close()
				return
			}

			conn := &Connection{
				logger:     logger,
				clientConn: clientConn,
				dockerConn: dockerConn,
			}

			conn.handleConnection(ctx)
		}()
	}
}
