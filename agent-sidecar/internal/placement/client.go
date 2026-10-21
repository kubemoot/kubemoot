package placement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/obs"
)

// Client calls the placement service over HTTP with a kept-alive connection. It sets no overall
// timeout: a placement may wait for capacity, and the caller's context ends the wait.
type Client struct {
	base string
	http *http.Client
}

// NewClient returns a client of the placement service at base (for example http://placement:8080).
func NewClient(base string) *Client {
	return &Client{base: base, http: &http.Client{Transport: obs.Transport(&http.Transport{
		MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 0,
	}, "placement")}}
}

// waitPause is the least time between two asks about a held call, so a service that answers 202 at once
// is not asked in a tight loop. The service holds the call; this is a floor, not a poll interval.
var waitPause = 100 * time.Millisecond

func pause(ctx context.Context) error {
	t := time.NewTimer(waitPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// reply is a decoded service answer.
type reply struct {
	status int
	body   []byte
}

// Place implements Placer. A 200 is the placement; a 409 is a no-fit with its kind; a 202 means
// the service holds the call for capacity: onQueued is called once, and the client then follows
// the wait URL until the service answers. A transport error or a 5xx is ErrUnavailable.
func (c *Client) Place(ctx context.Context, r Request, onQueued func(string)) (Reply, error) {
	r.Wait = true
	rep, err := c.do(ctx, http.MethodPost, "/placement/v1/place", r)
	if err == nil && rep.status == http.StatusAccepted {
		rep, err = c.follow(ctx, rep, onQueued)
	}
	if err != nil {
		return Reply{}, err
	}
	return decodePlace(rep)
}

// follow waits out a held call: it tells the owner once, then asks the wait URL until the service
// answers with something other than "still waiting".
func (c *Client) follow(ctx context.Context, rep reply, onQueued func(string)) (reply, error) {
	notified := false
	for rep.status == http.StatusAccepted {
		var w struct {
			WaitID string `json:"waitId"`
			Reason string `json:"reason"`
		}
		if jerr := json.Unmarshal(rep.body, &w); jerr != nil || w.WaitID == "" {
			return reply{}, &ErrUnavailable{Err: fmt.Errorf("a 202 without a waitId: %s", bytes.TrimSpace(rep.body))}
		}
		if !notified && onQueued != nil {
			onQueued(w.Reason)
		}
		notified = true
		if err := pause(ctx); err != nil {
			return reply{}, err
		}
		var err error
		if rep, err = c.do(ctx, http.MethodGet, "/placement/v1/wait/"+url.PathEscape(w.WaitID), nil); err != nil {
			return reply{}, err
		}
		if rep.status == http.StatusNotFound || rep.status == http.StatusGone {
			return reply{}, &ErrUnavailable{Err: fmt.Errorf("the placement service forgot the wait (%d)", rep.status)}
		}
	}
	return rep, nil
}

func decodePlace(rep reply) (Reply, error) {
	switch {
	case rep.status == http.StatusOK:
		var out Reply
		if err := json.Unmarshal(rep.body, &out); err != nil {
			return Reply{}, &ErrUnavailable{Err: err}
		}
		return out, nil
	case rep.status == http.StatusConflict:
		return Reply{}, parseNoFit(rep.body)
	case rep.status >= http.StatusInternalServerError:
		return Reply{}, &ErrUnavailable{Err: fmt.Errorf("answered %d: %s", rep.status, bytes.TrimSpace(rep.body))}
	default:
		return Reply{}, fmt.Errorf("placement service answered %d: %s", rep.status, bytes.TrimSpace(rep.body))
	}
}

// parseNoFit reads a 409. The kind is the body's kind, or the reason when that is one of the kinds.
func parseNoFit(body []byte) *ErrNoFit {
	var e struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	out := &ErrNoFit{Kind: e.Kind, Reason: e.Reason}
	if out.Kind == "" {
		switch e.Reason {
		case KindModelTooLarge, KindGPUBusy, KindPromptTooLarge:
			out.Kind = e.Reason
		}
	}
	return out
}

// Release implements Placer.
func (c *Client) Release(ctx context.Context, r Release) error {
	if r.Ticket == "" {
		return nil
	}
	rep, err := c.do(ctx, http.MethodPost, "/placement/v1/release", r)
	if err != nil {
		return err
	}
	if rep.status >= http.StatusMultipleChoices {
		return fmt.Errorf("release answered %d: %s", rep.status, bytes.TrimSpace(rep.body))
	}
	return nil
}

// Plan implements Planner.
func (c *Client) Plan(ctx context.Context, r PlanRequest) error {
	rep, err := c.do(ctx, http.MethodPost, "/placement/v1/plans", r)
	if err != nil {
		return err
	}
	if rep.status >= http.StatusMultipleChoices {
		return fmt.Errorf("plan answered %d: %s", rep.status, bytes.TrimSpace(rep.body))
	}
	return nil
}

// do sends one request; a transport failure is ErrUnavailable.
func (c *Client) do(ctx context.Context, method, path string, v any) (reply, error) {
	var body io.Reader
	if v != nil {
		payload, err := json.Marshal(v)
		if err != nil {
			return reply{}, err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return reply{}, err
	}
	if v != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return reply{}, ctx.Err()
		}
		return reply{}, &ErrUnavailable{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, &ErrUnavailable{Err: err}
	}
	return reply{status: resp.StatusCode, body: data}, nil
}
