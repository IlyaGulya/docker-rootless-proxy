package router

import (
	"context"
	"docker-socket-router/config"
	"fmt"
	"net"
	"os"

	"go.uber.org/fx"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

type Router struct {
	logger *zap.Logger
	config *config.SocketConfig
}

func NewRouter(logger *zap.Logger, config *config.SocketConfig) *Router {
	return &Router{
		logger: logger,
		config: config,
	}
}

func (r *Router) getUserUid(conn net.Conn) (uint32, error) {
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

func (r *Router) Start(lc fx.Lifecycle) error {
	socketMgr := newSocketManager(r.config.SystemSocket)

	// Try to acquire socket
	acquired, err := socketMgr.acquireSocket()
	if err != nil {
		return fmt.Errorf("failed to acquire socket: %v", err)
	}
	if !acquired {
		return fmt.Errorf("socket %s is in use by another process", r.config.SystemSocket)
	}

	// Create new socket
	listener, err := net.Listen("unix", r.config.SystemSocket)
	if err != nil {
		socketMgr.releaseSocket() // Cleanup on failure
		return fmt.Errorf("failed to create listener: %v", err)
	}

	// Set permissions
	if err := os.Chmod(r.config.SystemSocket, 0666); err != nil {
		listener.Close()
		socketMgr.releaseSocket()
		return fmt.Errorf("failed to set socket permissions: %v", err)
	}

	r.logger.Info("listening", zap.String("socket", r.config.SystemSocket))

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go r.acceptConnections(listener)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if err := listener.Close(); err != nil {
				r.logger.Error("failed to close listener", zap.Error(err))
			}
			return socketMgr.releaseSocket()
		},
	})

	return nil
}

func (r *Router) acceptConnections(listener net.Listener) {
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			r.logger.Error("accept error", zap.Error(err))
			continue
		}

		go r.handleConnection(clientConn)
	}
}

func (r *Router) handleConnection(clientConn net.Conn) {
	uid, err := r.getUserUid(clientConn)
	if err != nil {
		r.logger.Error("failed to get user credentials", zap.Error(err))
		clientConn.Close()
		return
	}

	userSocket := fmt.Sprintf(r.config.RootlessSocketFormat, uid)
	r.logger.Info("new connection",
		zap.Uint32("uid", uid),
		zap.String("user_socket", userSocket))

	dockerConn, err := net.Dial("unix", userSocket)
	if err != nil {
		r.logger.Error("failed to connect to user socket",
			zap.String("socket", userSocket),
			zap.Error(err))
		clientConn.Close()
		return
	}

	conn := NewConnection(r.logger, clientConn, dockerConn)
	conn.Handle(context.Background())
}
