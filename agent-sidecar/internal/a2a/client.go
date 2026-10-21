// Package a2a is the sidecar's side of the turn door: A2A 1.0 over the HTTP+JSON binding, through
// the official a2a-go v2 client. The harness is the A2A server; the sidecar hands it one turn and
// takes one result. This package builds the turn, reduces the harness's events to one result, and
// turns that result into a signal.
package a2a

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// ProfileURI names the Kubemoot turn profile carried in data parts.
const ProfileURI = "https://kubemoot.org/a2a/turn/v1"

// profilePrefix is how every version of the profile starts; a card naming another version is a mismatch.
const profilePrefix = "https://kubemoot.org/a2a/turn/"

// ErrContractMismatch means the harness speaks a turn profile the sidecar does not serve.
var ErrContractMismatch = errors.New("the harness speaks another turn profile")

// Client talks to one harness.
type Client struct {
	c         *a2aclient.Client
	card      *sdk.AgentCard
	streaming bool
}

// Discover fetches the harness's agent card at root, checks its turn profile, and returns a client
// for the HTTP+JSON interface the card declares. hc carries the trace context to the harness.
func Discover(ctx context.Context, root string, hc *http.Client) (*Client, error) {
	card, err := agentcard.NewResolver(hc).Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := CheckProfile(card); err != nil {
		return nil, err
	}
	c, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithRESTTransport(hc),
		a2aclient.WithConfig(a2aclient.Config{PreferredTransports: []sdk.TransportProtocol{sdk.TransportProtocolHTTPJSON}}))
	if err != nil {
		return nil, fmt.Errorf("agent card: %w", err)
	}
	return &Client{c: c, card: card, streaming: card.Capabilities.Streaming}, nil
}

// CheckProfile refuses a card that declares a turn profile other than v1. A card that declares
// none is a level 1 harness and is taken to speak v1.
func CheckProfile(card *sdk.AgentCard) error {
	var declared []string
	for _, e := range card.Capabilities.Extensions {
		if strings.HasPrefix(e.URI, profilePrefix) {
			if e.URI == ProfileURI {
				return nil
			}
			declared = append(declared, e.URI)
		}
	}
	if len(declared) > 0 {
		return fmt.Errorf("%w: it declares %s, this sidecar serves %s", ErrContractMismatch, declared[0], ProfileURI)
	}
	return nil
}

// Alive reports whether the harness's card still answers; readiness follows it.
func Alive(ctx context.Context, root string, hc *http.Client) bool {
	_, err := agentcard.NewResolver(hc).Resolve(ctx, root)
	return err == nil
}

// Result is the harness's reply, reduced from the A2A events it sent.
type Result struct {
	TaskID  sdk.TaskID
	State   sdk.TaskState
	Parts   []*sdk.Part
	Message *sdk.Message
}

// Do hands the harness one message and returns its reduced reply. onTask, when not nil, is called
// with the task id as soon as the harness names it, so the turn can be canceled. A harness that
// announces streaming is sent a streaming message, so the task id arrives early; any other gets a
// blocking send.
func (c *Client) Do(ctx context.Context, msg *sdk.Message, onTask func(sdk.TaskID)) (Result, error) {
	req := &sdk.SendMessageRequest{Message: msg}
	if c.streaming {
		return c.stream(ctx, req, onTask)
	}
	got, err := c.c.SendMessage(ctx, req)
	if err != nil {
		return Result{}, err
	}
	var r Result
	reduce(&r, got.(sdk.Event), onTask)
	return r, nil
}

func (c *Client) stream(ctx context.Context, req *sdk.SendMessageRequest, onTask func(sdk.TaskID)) (Result, error) {
	var r Result
	return r, drain(&r, c.c.SendStreamingMessage(ctx, req), onTask)
}

// drain applies every event to r until the stream ends or errors.
func drain(r *Result, events iter.Seq2[sdk.Event, error], onTask func(sdk.TaskID)) error {
	for ev, err := range events {
		if err != nil {
			return err
		}
		reduce(r, ev, onTask)
	}
	return nil
}

// Cancel asks the harness to stop a task. It is best effort: a harness that cannot cancel finishes
// on its own, and the sidecar has already stopped waiting for it.
func (c *Client) Cancel(ctx context.Context, id sdk.TaskID) error {
	_, err := c.c.CancelTask(ctx, &sdk.CancelTaskRequest{ID: id})
	return err
}

// reduce folds one event into the result.
func reduce(r *Result, ev sdk.Event, onTask func(sdk.TaskID)) {
	switch e := ev.(type) {
	case *sdk.Message:
		r.Message = e
		r.State = sdk.TaskStateCompleted
	case *sdk.Task:
		note(r, e.ID, onTask)
		r.State = e.Status.State
		r.Message = e.Status.Message
		r.Parts = r.Parts[:0]
		for _, a := range e.Artifacts {
			r.Parts = append(r.Parts, a.Parts...)
		}
	case *sdk.TaskStatusUpdateEvent:
		note(r, e.TaskID, onTask)
		r.State = e.Status.State
		if e.Status.Message != nil {
			r.Message = e.Status.Message
		}
	case *sdk.TaskArtifactUpdateEvent:
		note(r, e.TaskID, onTask)
		if e.Artifact != nil {
			r.Parts = append(r.Parts, e.Artifact.Parts...)
		}
	}
}

func note(r *Result, id sdk.TaskID, onTask func(sdk.TaskID)) {
	if id == "" || r.TaskID == id {
		return
	}
	r.TaskID = id
	if onTask != nil {
		onTask(id)
	}
}
