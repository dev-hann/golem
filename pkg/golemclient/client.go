package golemclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// recvResult carries one Receive outcome from the reader goroutine.
type recvResult struct {
	ev  any
	err error
}

// Client is a blocking NDJSON client over any net.Conn (unix socket, TCP).
// A single internal reader goroutine feeds an event channel, so Receive and
// ReceiveWithTimeout can interleave safely from one goroutine, and Send may
// run concurrently.
type Client struct {
	conn net.Conn

	writeMu   sync.Mutex
	startOnce sync.Once
	events    chan recvResult
}

// eventBuffer bounds queued inbound events.
const eventBuffer = 64

// Dial connects to a golem server over a unix socket path.
func Dial(socketPath string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("golemclient: dialing %s: %w", socketPath, err)
	}

	return NewClient(conn), nil
}

// NewClient wraps an established connection.
func NewClient(conn net.Conn) *Client {
	return &Client{conn: conn, events: make(chan recvResult, eventBuffer)}
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("golemclient: closing: %w", err)
	}

	return nil
}

// Send marshals one client event as an NDJSON line.
func (c *Client) Send(event any) error {
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("golemclient: marshaling %T: %w", event, err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("golemclient: writing: %w", err)
	}

	return nil
}

// Receive blocks for the next server event, decoding it into a typed value.
// It returns an error when the connection ends.
func (c *Client) Receive() (any, error) {
	r := <-c.read()
	return r.ev, r.err
}

// ReceiveWithTimeout waits for the next event up to the timeout. It returns
// false when the deadline elapsed with no event (the event, if it arrives
// later, is delivered by a subsequent Receive).
func (c *Client) ReceiveWithTimeout(timeout time.Duration) (ev any, got bool, err error) {
	select {
	case r := <-c.read():
		return r.ev, true, r.err
	case <-time.After(timeout):
		return nil, false, nil
	}
}

// read lazily starts the reader loop and returns its channel.
func (c *Client) read() (ch <-chan recvResult) {
	c.startOnce.Do(func() { go c.receiveLoop() })
	return c.events
}

// receiveLoop decodes NDJSON lines into the event channel until the
// connection errors.
func (c *Client) receiveLoop() {
	reader := bufio.NewReader(c.conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			c.events <- recvResult{nil, fmt.Errorf("golemclient: reading: %w", err)}
			return
		}

		ev, decodeErr := Decode(line)
		c.events <- recvResult{ev: ev, err: decodeErr}
	}
}
