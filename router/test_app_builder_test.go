package router

import (
	"context"
	"docker-socket-router/config"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/zap"
)

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

// Build constructs the Fx test application.
func (b *TestAppBuilder) Build() *fxtest.App {
	return fxtest.New(b.t,
		fx.Provide(
			func() *zap.Logger { return b.logger },
			func() *config.SocketConfig { return b.cfg },
			func() Dialer { return b.dialer },
			// Provide the Router using the standard constructor.
			func(logger *zap.Logger, cfg *config.SocketConfig, dialer Dialer) *Router {
				return NewRouter(logger, cfg, dialer)
			},
		),
		fx.Invoke(func(lc fx.Lifecycle, r *Router) error {
			return r.Start(lc)
		}),
		fx.Options(b.extraOpts...),
	)
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
