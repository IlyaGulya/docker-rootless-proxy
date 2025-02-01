package router

import (
	"net"
	"time"
)

// Dialer is an interface to dial a connection. It is used so that we can inject
// alternative implementations (for example, a mock that simulates errors).
type Dialer interface {
	Dial(network, address string) (net.Conn, error)
}

// defaultDialer is the production implementation that wraps net.Dialer.
type defaultDialer struct {
	timeout time.Duration
}

// Dial dials the given network/address using net.Dialer with a timeout.
func (d *defaultDialer) Dial(network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: d.timeout}
	return dialer.Dial(network, address)
}

// NewDefaultDialer returns a Dialer with a default timeout.
func NewDefaultDialer() Dialer {
	return &defaultDialer{timeout: 250 * time.Millisecond}
}
