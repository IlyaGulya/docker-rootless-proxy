package router

import (
	"context"
	"docker-socket-router/apierror"
	"docker-socket-router/config"
	"fmt"
	"net"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

type Router struct {
	logger *zap.Logger
	config *config.SocketConfig
	dialer Dialer
}

// NewRouter now requires a Dialer (in addition to logger and config).
func NewRouter(logger *zap.Logger, config *config.SocketConfig, dialer Dialer) *Router {
	return &Router{
		logger: logger,
		config: config,
		dialer: dialer,
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

	// Acquire the system socket
	acquired, err := socketMgr.acquireSocket()
	if err != nil {
		return fmt.Errorf("failed to acquire socket: %w", err)
	}
	if !acquired {
		return fmt.Errorf("socket %s is in use by another process", r.config.SystemSocket)
	}

	// Create the Unix domain socket listener
	listener, err := net.Listen("unix", r.config.SystemSocket)
	if err != nil {
		releaseErr := socketMgr.releaseSocket()
		return multierr.Combine(
			fmt.Errorf("failed to create listener: %w", err),
			releaseErr,
		)
	}

	// Set socket permissions to allow all users to connect
	if err := os.Chmod(r.config.SystemSocket, 0666); err != nil {
		return multierr.Combine(
			fmt.Errorf("failed to set socket permissions: %w", err),
			listener.Close(),
			socketMgr.releaseSocket(),
		)
	}

	r.logger.Info("listening", zap.String("socket", r.config.SystemSocket))

	// Using fx lifecycle hooks with errgroup for concurrent shutdown
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go r.acceptConnections(listener)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			g := errgroup.Group{}

			g.Go(func() error {
				return listener.Close()
			})

			g.Go(func() error {
				return socketMgr.releaseSocket()
			})

			return g.Wait()
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
	defer clientConn.Close()

	uid, err := r.getUserUid(clientConn)
	if err != nil {
		r.logger.Error("failed to get user credentials", zap.Error(err))
		// Write an HTTP 500 JSON error, then return
		if writeErr := r.writeErrorResponse(
			clientConn,
			"Permission denied while accessing Docker daemon",
			apierror.CodePermissionDenied,
		); writeErr != nil {
			r.logger.Error("failed to write error response", zap.Error(writeErr))
		}
		return
	}

	userSocket := fmt.Sprintf(r.config.RootlessSocketFormat, uid)
	r.logger.Info("new connection",
		zap.Uint32("uid", uid),
		zap.String("user_socket", userSocket),
	)

	// Instead of creating a net.Dialer here, use the injected dialer.
	dockerConn, err := r.dialer.Dial("unix", userSocket)
	if err != nil {
		r.logger.Error("failed to connect to user socket",
			zap.String("socket", userSocket),
			zap.Error(err),
		)

		var sysErr error
		if opErr, ok := err.(*net.OpError); ok {
			sysErr = opErr.Err
		} else {
			sysErr = err
		}

		var response string
		var code string
		switch {
		case os.IsNotExist(sysErr):
			response = fmt.Sprintf("Cannot connect to Docker daemon at %s", userSocket)
			code = apierror.CodeSocketNotFound

		case os.IsPermission(sysErr):
			response = "Permission denied while trying to connect to Docker daemon"
			code = apierror.CodePermissionDenied

		default:
			response = fmt.Sprintf("Error while connecting to Docker daemon: %v", err)
			code = apierror.CodeConnectionFailed
		}

		if writeErr := r.writeErrorResponse(clientConn, response, code); writeErr != nil {
			r.logger.Error("failed to write error response", zap.Error(writeErr))
		}
		return
	}

	conn := NewConnection(r.logger, clientConn, dockerConn)
	conn.Handle(context.Background())
}

func (r *Router) writeErrorResponse(conn net.Conn, message, code string) error {
	// Set a very short deadline for writing the error
	if err := conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	if err := apierror.WriteError(conn, message, code); err != nil {
		return fmt.Errorf("failed to write error: %w", err)
	}

	// Try to flush/close the connection cleanly
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0) // Don't wait on close
	}

	return nil
}
