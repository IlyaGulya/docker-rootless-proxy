package router

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type Connection struct {
	logger        *zap.Logger
	clientConn    net.Conn
	dockerConn    net.Conn
	bytesToDocker atomic.Int64
	fromDocker    atomic.Int64
}

func NewConnection(logger *zap.Logger, clientConn, dockerConn net.Conn) *Connection {
	return &Connection{
		logger:     logger,
		clientConn: clientConn,
		dockerConn: dockerConn,
	}
}

func (c *Connection) copyData(dst net.Conn, src net.Conn, bytes *atomic.Int64) error {
	written, err := io.Copy(dst, src)
	bytes.Add(written)
	return err
}

func (c *Connection) Handle(ctx context.Context) {
	defer c.clientConn.Close()
	defer c.dockerConn.Close()

	startTime := time.Now()

	// We want to wait for both directions
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		err := c.copyData(c.dockerConn, c.clientConn, &c.bytesToDocker)
		if err != nil && err != io.EOF {
			c.logger.Error("copy error client→docker", zap.Error(err))
		}
	}()

	go func() {
		defer wg.Done()
		err := c.copyData(c.clientConn, c.dockerConn, &c.fromDocker)
		if err != nil && err != io.EOF {
			c.logger.Error("copy error docker→client", zap.Error(err))
		}
	}()

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-ctx.Done():
		c.logger.Info("connection terminated by context")
	case <-doneCh:
		// Both goroutines finished
	}

	duration := time.Since(startTime)
	c.logger.Info("connection closed",
		zap.Duration("duration", duration),
		zap.Int64("bytes_to_docker", c.bytesToDocker.Load()),
		zap.Int64("bytes_from_docker", c.fromDocker.Load()),
	)
}
