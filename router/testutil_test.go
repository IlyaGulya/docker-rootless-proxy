package router

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// testLogBuffer is a thread-safe buffer for collecting test logs
type testLogBuffer struct {
	buf   bytes.Buffer
	mutex sync.Mutex
	t     *testing.T
}

func (b *testLogBuffer) Write(p []byte) (n int, err error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buf.Write(p)
}

// flushToTest safely writes all buffered logs to the test logger
func (b *testLogBuffer) flushToTest() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if b.buf.Len() > 0 {
		b.t.Log(b.buf.String())
		b.buf.Reset()
	}
}

// threadSafeTestLogger creates a zap logger that is safe for concurrent use in tests
func threadSafeTestLogger(t *testing.T) (*zap.Logger, func()) {
	// Create the log buffer
	buffer := &testLogBuffer{t: t}

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create the core
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.AddSync(buffer),
		zapcore.DebugLevel,
	)

	// Create the logger
	logger := zap.New(core)

	// Return the logger and a cleanup function that flushes logs
	cleanup := func() {
		logger.Sync()
		buffer.flushToTest()
	}

	return logger, cleanup
}

// withTestLogger runs a test function with a thread-safe logger and ensures logs are flushed
func withTestLogger(t *testing.T, fn func(*zap.Logger)) {
	logger, cleanup := threadSafeTestLogger(t)
	defer cleanup()
	fn(logger)
}

// testLogSync provides a way to sync logs during tests at specific points
type testLogSync struct {
	buffer  *testLogBuffer
	t       *testing.T
	timeout time.Duration
}

func newTestLogSync(buffer *testLogBuffer, t *testing.T) *testLogSync {
	return &testLogSync{
		buffer:  buffer,
		t:       t,
		timeout: 5 * time.Second,
	}
}

func (s *testLogSync) Sync() {
	done := make(chan struct{})
	go func() {
		s.buffer.flushToTest()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(s.timeout):
		s.t.Error("timeout waiting for logs to sync")
	}
}
