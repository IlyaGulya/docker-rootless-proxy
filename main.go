package main

import (
	"context"
	"docker-socket-router/version"
	"flag"
	"fmt"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

type Connection struct {
	logger        *zap.Logger
	clientConn    net.Conn
	dockerConn    net.Conn
	bytesToDocker atomic.Int64
	fromDocker    atomic.Int64
}

func (c *Connection) copyData(dst net.Conn, src net.Conn, bytes *atomic.Int64) error {
	written, err := io.Copy(dst, src)
	bytes.Add(written)
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

	select {
	case <-ctx.Done():
		c.logger.Info("connection terminated by context")
	case err := <-errChan:
		if err != nil && err != io.EOF {
			c.logger.Error("data transfer error", zap.Error(err))
		}
	}

	duration := time.Since(startTime)
	c.logger.Info("connection closed",
		zap.Duration("duration", duration),
		zap.Int64("bytes_to_docker", c.bytesToDocker.Load()),
		zap.Int64("bytes_from_docker", c.fromDocker.Load()))
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
		// Socket exists but can't connect
		return fmt.Errorf("socket %s exists but appears to be stale. Please check if another instance is running and remove the socket file if it's not in use", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking socket %s: %v", path, err)
	}
	return nil
}

func startRouter(ctx context.Context, config SocketConfig, logger *zap.Logger) error {
	// Check for existing socket before proceeding
	if err := checkExistingSocket(config.SystemSocket); err != nil {
		logger.Error("startup failed", zap.Error(err))
		return err
	}

	// Remove existing socket if present
	os.Remove(config.SystemSocket)

	listener, err := net.Listen("unix", config.SystemSocket)
	if err != nil {
		logger.Error("failed to create listener", zap.Error(err))
		return err
	}
	defer listener.Close()

	// Set socket permissions
	if err := os.Chmod(config.SystemSocket, 0666); err != nil {
		logger.Error("failed to set socket permissions", zap.Error(err))
		return err
	}

	logger.Info("listening", zap.String("socket", config.SystemSocket))

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // Normal shutdown
			}
			logger.Error("accept error", zap.Error(err))
			continue
		}

		go func() {
			uid, err := getUserUid(clientConn)
			if err != nil {
				logger.Error("failed to get user credentials", zap.Error(err))
				clientConn.Close()
				return
			}

			userSocket := fmt.Sprintf(config.RootlessSocketFormat, uid)
			logger.Info("new connection",
				zap.Uint32("uid", uid),
				zap.String("user_socket", userSocket))

			dockerConn, err := net.Dial("unix", userSocket)
			if err != nil {
				logger.Error("failed to connect to user socket",
					zap.String("socket", userSocket),
					zap.Error(err))
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

func main() {
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println(version.Info())
		return
	}

	logger, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("failed to initialize logger: %v", err))
	}
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			fmt.Printf("failed to flush logger: %v\n", err)
		}
	}(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Info("received shutdown signal")
		cancel()
	}()

	logger.Info("starting docker-socket-router", zap.String("version", version.Version()))

	if err := startRouter(ctx, defaultConfig, logger); err != nil {
		os.Exit(1)
	}
}
