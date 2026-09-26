package nats

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("nats-client")

// Client provides a lazy NATS connection with JetStream access.
type Client struct {
	mu   sync.Mutex
	conn *nats.Conn
	js   jetstream.JetStream
	url  string
}

// NewClient creates a client that connects lazily on first use.
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

// connect establishes the connection lazily. Must be called under lock.
func (c *Client) connect() error {
	if c.conn != nil && c.conn.IsConnected() {
		return nil
	}

	opts := []nats.Option{
		nats.Name("discussion-gateway"),
		nats.ReconnectWait(5 * time.Second),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.V(1).Info("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.V(1).Info("NATS reconnected")
		}),
	}

	conn, err := nats.Connect(c.url, opts...)
	if err != nil {
		return err
	}

	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return err
	}

	c.conn = conn
	c.js = js
	log.Info("Connected to NATS", "url", c.url)
	return nil
}

// JetStream returns the JetStream context, connecting lazily.
func (c *Client) JetStream() (jetstream.JetStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connect(); err != nil {
		return nil, err
	}
	return c.js, nil
}

// IsConfigured returns true if a NATS URL is set.
func (c *Client) IsConfigured() bool {
	return c.url != ""
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
		c.conn.Drain()
		c.conn = nil
		c.js = nil
	}
}
