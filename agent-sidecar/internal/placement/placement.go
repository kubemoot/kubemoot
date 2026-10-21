// Package placement is the sidecar's client of the placement service: one call asks where a
// model call runs and returns a ticket; a second call gives the ticket back.
package placement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Request asks where one model call runs. Models are the acceptable models, preferred first.
type Request struct {
	Namespace       string   `json:"namespace,omitempty"`
	Crew            string   `json:"crew"`
	Agent           string   `json:"agent"`
	ThreadID        string   `json:"threadId,omitempty"`
	Phase           string   `json:"phase,omitempty"`
	Models          []string `json:"models"`
	PromptTokens    int64    `json:"promptTokens,omitempty"`
	MaxOutputTokens int64    `json:"maxOutputTokens,omitempty"`
	MayUnload       bool     `json:"mayUnload"`
}

// Reply is where the call runs. Ticket is given back with Release when the call ends.
type Reply struct {
	Server   string   `json:"server"`
	Endpoint string   `json:"endpoint"`
	Model    string   `json:"model"`
	Reason   string   `json:"reason"`
	Ticket   string   `json:"ticket"`
	Queued   bool     `json:"queued"`
	ColdLoad bool     `json:"coldLoad"`
	Evicted  []string `json:"evicted"`
}

// ErrNoFit means no server has room for the call now; Reason says why.
type ErrNoFit struct{ Reason string }

func (e *ErrNoFit) Error() string { return "kubemoot_no_fit: " + e.Reason }

// Placer is the seam the model door depends on. The placement service is one implementation.
type Placer interface {
	Place(ctx context.Context, r Request) (Reply, error)
	Release(ctx context.Context, ticket string) error
}

// Bound is the placer for an agent bound to one server: no service call, no ticket. It is the design's
// bound mode and the floor the service hop is measured against.
type Bound struct{ Server, Endpoint string }

// Place implements Placer with the first acceptable model on the bound server.
func (b Bound) Place(_ context.Context, r Request) (Reply, error) {
	if len(r.Models) == 0 {
		return Reply{}, &ErrNoFit{Reason: "no model requested"}
	}
	return Reply{Server: b.Server, Endpoint: b.Endpoint, Model: r.Models[0], Reason: "bound", Evicted: []string{}}, nil
}

// Release implements Placer; a bound call holds no ticket.
func (Bound) Release(context.Context, string) error { return nil }

// Client calls the placement service over HTTP with a kept-alive connection.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a client of the placement service at base (for example http://placement:8080).
func NewClient(base string) *Client {
	return &Client{base: base, http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
	}}}
}

// Place implements Placer.
func (c *Client) Place(ctx context.Context, r Request) (Reply, error) {
	var out Reply
	status, body, err := c.post(ctx, "/placement/v1/place", r)
	if err != nil {
		return out, err
	}
	switch status {
	case http.StatusOK:
		return out, json.Unmarshal(body, &out)
	case http.StatusConflict:
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &e)
		return out, &ErrNoFit{Reason: e.Reason}
	default:
		return out, fmt.Errorf("placement service answered %d: %s", status, bytes.TrimSpace(body))
	}
}

// Release implements Placer.
func (c *Client) Release(ctx context.Context, ticket string) error {
	if ticket == "" {
		return nil
	}
	status, body, err := c.post(ctx, "/placement/v1/release", map[string]string{"ticket": ticket})
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("release answered %d: %s", status, bytes.TrimSpace(body))
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, v any) (int, []byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, errors.Join(errors.New("placement service unreachable"), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}
