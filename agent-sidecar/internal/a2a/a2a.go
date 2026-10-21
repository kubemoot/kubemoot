// Package a2a is the sidecar's client of the turn door: A2A 1.0 over the HTTP+JSON binding, the subset
// the design uses (agent card, SendMessage). The harness is the A2A server.
package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ContentType is the media type A2A 1.0 requests and replies use on the HTTP+JSON binding.
	ContentType = "application/a2a+json"
	// Version is the A2A major.minor the client speaks.
	Version = "1.0"
	// BindingHTTPJSON is the protocolBinding value of the HTTP+JSON binding.
	BindingHTTPJSON = "HTTP+JSON"
	// ProfileURI names the Kubemoot turn profile carried in data parts.
	ProfileURI = "https://kubemoot.org/a2a/turn/v1"
)

// Card is the part of an A2A agent card the sidecar reads.
type Card struct {
	Name                string      `json:"name"`
	SupportedInterfaces []Interface `json:"supportedInterfaces"`
	Capabilities        struct {
		Streaming bool `json:"streaming"`
	} `json:"capabilities"`
}

// Interface is one way to reach the agent.
type Interface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// Part is one part of a message or artifact: exactly one of Text or Data is set.
type Part struct {
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Message is an A2A message.
type Message struct {
	MessageID string `json:"messageId"`
	ContextID string `json:"contextId,omitempty"`
	Role      string `json:"role"`
	Parts     []Part `json:"parts"`
}

// Artifact is a task output.
type Artifact struct {
	Parts []Part `json:"parts"`
}

// Task is the reply to SendMessage.
type Task struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	Status    struct {
		State   string   `json:"state"`
		Message *Message `json:"message,omitempty"`
	} `json:"status"`
	Artifacts []Artifact `json:"artifacts"`
}

// SendResponse is the SendMessage reply: a task, or a message for an agent that answers at once.
type SendResponse struct {
	Task    *Task    `json:"task,omitempty"`
	Message *Message `json:"message,omitempty"`
}

// Client talks to one harness.
type Client struct {
	base string
	http *http.Client
}

// Discover fetches the agent card at root and returns a client for the HTTP+JSON interface it declares.
// Interfaces are tried in the card's order; the first HTTP+JSON one wins.
func Discover(ctx context.Context, root string) (*Client, Card, error) {
	var card Card
	url := strings.TrimRight(root, "/") + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, card, err
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, card, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, card, fmt.Errorf("agent card answered %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, card, err
	}
	for _, i := range card.SupportedInterfaces {
		if i.ProtocolBinding == BindingHTTPJSON {
			return &Client{base: strings.TrimRight(i.URL, "/"), http: &http.Client{}}, card, nil
		}
	}
	return nil, card, errors.New("agent card declares no HTTP+JSON interface")
}

// Send hands the harness one message and returns its reply. The deadline travels in ctx.
func (c *Client) Send(ctx context.Context, m Message) (SendResponse, error) {
	var out SendResponse
	payload, err := json.Marshal(map[string]any{"message": m})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/message:send", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", ContentType)
	req.Header.Set("A2A-Version", Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("SendMessage answered %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	return out, json.Unmarshal(body, &out)
}
