package router

import (
	"context"
	"docker-socket-router/config"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/zap"
)

// testSocketManager is a more aggressive version of socketManager for testing.
// It will forcefully clean up sockets and pid files.
type testSocketManager struct {
	*socketManager
	mu       sync.Mutex
	acquired bool
}

func newTestSocketManager(socketPath string) *testSocketManager {
	// Clean up any existing files first
	os.Remove(socketPath)
	os.Remove(socketPath + ".pid")

	return &testSocketManager{
		socketManager: newSocketManager(socketPath),
		acquired:      false,
	}
}

// cleanup forcefully removes socket and pid files
func (sm *testSocketManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Force cleanup regardless of state
	os.Remove(sm.socketPath)
	os.Remove(sm.pidFile)
	sm.acquired = false
}

// acquireSocket overrides the default behavior to be more aggressive in cleanup
func (sm *testSocketManager) acquireSocket() (bool, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Always try to clean up any existing files first
	sm.cleanup()

	// Check if already acquired
	if sm.acquired {
		return false, fmt.Errorf("socket %s is in use by process %d", sm.socketPath, os.Getpid())
	}

	// Create PID file with exclusive flag
	f, err := os.OpenFile(sm.pidFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			return false, fmt.Errorf("pid file %s was just created by another process", sm.pidFile)
		}
		return false, fmt.Errorf("failed to create pid file: %v", err)
	}
	defer f.Close()

	// Write our PID
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		os.Remove(sm.pidFile)
		return false, fmt.Errorf("failed to write pid file: %v", err)
	}

	sm.acquired = true
	return true, nil
}

// releaseSocket overrides the default behavior to handle mutex
func (sm *testSocketManager) releaseSocket() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.acquired {
		sm.cleanup()
	}
	return nil
}

// TestAppBuilder is a helper to construct a test Fx application for the Router.
// It provides default dependencies (logger, config, dialer) but lets tests override any of them.
type TestAppBuilder struct {
	t         testing.TB
	logger    *zap.Logger
	cfg       *config.SocketConfig
	dialer    Dialer
	extraOpts []fx.Option
}

// NewTestAppBuilder creates a new TestAppBuilder using default dependencies.
func NewTestAppBuilder(t testing.TB, cfg *config.SocketConfig) *TestAppBuilder {
	logger, _ := threadSafeTestLogger(t)
	return &TestAppBuilder{
		t:      t,
		logger: logger,
		cfg:    cfg,
		dialer: NewDefaultDialer(),
	}
}

// WithDialer allows substituting the default Dialer.
func (b *TestAppBuilder) WithDialer(d Dialer) *TestAppBuilder {
	b.dialer = d
	return b
}

// WithOptions appends additional Fx options to the test application.
func (b *TestAppBuilder) WithOptions(opts ...fx.Option) *TestAppBuilder {
	b.extraOpts = append(b.extraOpts, opts...)
	return b
}

func cleanupSocket(socketPath string) {
	// First try to remove the socket file
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		// If removal failed for any reason other than not existing,
		// wait a short time and try again with force
		time.Sleep(50 * time.Millisecond)
		// Try to chmod it first in case it's permission issue
		os.Chmod(socketPath, 0666)
		os.Remove(socketPath)
	}

	// Then remove the PID file
	pidFile := socketPath + ".pid"
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		time.Sleep(50 * time.Millisecond)
		// Try to chmod it first in case it's permission issue
		os.Chmod(pidFile, 0666)
		os.Remove(pidFile)
	}

	// Verify cleanup was successful
	if _, err := os.Stat(socketPath); err == nil {
		// Socket still exists, try one last time with longer wait
		time.Sleep(100 * time.Millisecond)
		os.Remove(socketPath)
	}
}

// Build constructs the Fx test application.
func (b *TestAppBuilder) Build() *fxtest.App {
	// Ensure socket is cleaned up before starting
	cleanupSocket(b.cfg.SystemSocket)

	return fxtest.New(b.t,
		fx.Provide(
			func() *zap.Logger { return b.logger },
			func() *config.SocketConfig { return b.cfg },
			func() Dialer { return b.dialer },
			func(logger *zap.Logger, cfg *config.SocketConfig, dialer Dialer) *Router {
				// Use the test socket manager for tests
				socketMgr := newTestSocketManager(cfg.SystemSocket).socketManager
				return NewRouter(logger, cfg, dialer, socketMgr)
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, r *Router) error {
			return r.Start(lc)
		}),
		fx.Options(b.extraOpts...),
	)
}

func (b *TestAppBuilder) TryStart(ctx context.Context) (stopFn func(), err error) {
	// Build our Fx test application (same as in Start).
	app := b.Build()

	// Attempt to start the application. If it fails, return the error
	// so the caller can decide what to do (instead of calling t.Fatalf).
	if startErr := app.Start(ctx); startErr != nil {
		return nil, startErr
	}

	// If we get here, the app successfully started.
	// Return a cleanup function that stops it.
	stopFn = func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		if stopErr := app.Stop(stopCtx); stopErr != nil {
			// We still log the stop error to the test, but we do not abort the entire suite.
			b.t.Errorf("Failed to stop Fx app: %v", stopErr)
		}
	}
	return stopFn, nil
}

// Start builds and starts the Fx test application.
// It returns a stop function that, when called, stops the application.
func (b *TestAppBuilder) Start(ctx context.Context) (stopFn func()) {
	app := b.Build()
	if err := app.Start(ctx); err != nil {
		b.t.Fatalf("Failed to start Fx app: %v", err)
	}
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		if err := app.Stop(stopCtx); err != nil {
			b.t.Errorf("Failed to stop Fx app: %v", err)
		}
	}
}
