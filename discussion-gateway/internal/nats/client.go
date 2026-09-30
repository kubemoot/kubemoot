package nats

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("nats-client")

// ErrNotConnected is returned while the connection to NATS is not up.
var ErrNotConnected = errors.New("not connected to NATS")

// Client holds one NATS connection with JetStream access. Start opens it; the NATS
// library then keeps it open, reconnecting in the background after a drop.
type Client struct {
	mu   sync.Mutex
	conn *nats.Conn
	js   jetstream.JetStream
	url  string
}

// NewClient creates a client for url, or for NATS_URL when url is empty.
func NewClient(url string) *Client {
	if url == "" {
		url = os.Getenv("NATS_URL")
	}
	return &Client{url: url}
}

// URL returns the configured NATS URL.
func (c *Client) URL() string {
	return c.url
}

// IsConfigured returns true if a NATS URL is set.
func (c *Client) IsConfigured() bool {
	return c.url != ""
}

// Start opens the connection. When the server is unreachable it keeps retrying in the
// background, so the gateway can start before NATS and turn ready when it connects.
func (c *Client) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil || c.url == "" {
		return nil
	}

	conn, err := nats.Connect(c.url,
		nats.Name("discussion-gateway"),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
		nats.ConnectHandler(func(_ *nats.Conn) { log.Info("Connected to NATS", "url", c.url) }),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Info("NATS disconnected", "error", err.Error())
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) { log.Info("NATS reconnected") }),
	)
	if err != nil {
		return err
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return err
	}
	c.conn, c.js = conn, js
	return nil
}

// Connected reports whether the connection to NATS is up now.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && c.conn.IsConnected()
}

// JetStream returns the JetStream context, opening the connection if Start has not.
// It fails while the connection is down, so a caller never waits on a dead link.
func (c *Client) JetStream() (jetstream.JetStream, error) {
	if err := c.Start(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.js == nil || !c.conn.IsConnected() {
		return nil, ErrNotConnected
	}
	return c.js, nil
}

// Publish publishes a message to JetStream on the given subject.
// Returns nil (no-op) when NATS is not configured.
func (c *Client) Publish(ctx context.Context, subject string, data []byte) error {
	if c.url == "" {
		return nil
	}

	js, err := c.JetStream()
	if err != nil {
		return err
	}

	_, err = js.Publish(ctx, subject, data)
	return err
}

// Close drains and closes the connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Drain(); err != nil {
			log.Info("Could not drain the NATS connection", "error", err.Error())
		}
		c.conn = nil
		c.js = nil
	}
}
