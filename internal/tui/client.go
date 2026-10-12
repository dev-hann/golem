// Package tui implements golem's terminal client (Bubble Tea). The model is
// testable without a terminal: events are fed via Update and the View is
// asserted as plain strings. All client I/O goes through the client
// interface seam satisfied by pkg/golemclient.
package tui

import (
	"github.com/dev-hann/golem/pkg/golemclient"
)

// client is the seam between the TUI and a golem server connection.
// *golemclient.Client satisfies it; tests substitute a fake.
type client interface {
	Send(event any) error
	Receive() (any, error)
	Close() error
}

// dialer opens client connections (overridable in tests).
type dialer func(socketPath string) (client, error)

// realDial connects via golemclient.
func realDial(socketPath string) (client, error) {
	return golemclient.Dial(socketPath)
}
