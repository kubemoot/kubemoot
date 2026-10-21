// Package placement is the sidecar's client of the placement service. The service decides, per
// model call, which server and model run it and hands out a ticket; the sidecar gives the ticket
// back when the call ends. The API (see the README) is the seam: the service can be replaced
// without a sidecar change.
package placement

import (
	"context"
	"errors"
	"fmt"
)

// The kinds of a call no server can take, as the service names them and as the bus names a
// capacity stand-aside.
const (
	KindModelTooLarge  = "model-too-large"
	KindGPUBusy        = "gpu-busy"
	KindPromptTooLarge = "prompt-too-large"
)

// Request asks where one model call runs. Models are the acceptable models, preferred first.
type Request struct {
	Namespace string `json:"namespace,omitempty"`
	Crew      string `json:"crew"`
	Agent     string `json:"agent"`
	// ThreadID lets the service hand back the plan it made when the agent was selected.
	ThreadID        string   `json:"threadId,omitempty"`
	Phase           string   `json:"phase,omitempty"`
	Alias           string   `json:"alias,omitempty"`
	Models          []string `json:"models"`
	PromptTokens    int64    `json:"promptTokens,omitempty"`
	MaxOutputTokens int64    `json:"maxOutputTokens,omitempty"`
	MayUnload       bool     `json:"mayUnload"`
	// Wait asks the service to hold the call until capacity frees instead of answering no-fit at
	// once. The caller's disconnect ends the wait.
	Wait bool `json:"wait,omitempty"`
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
	// Degraded is set by the sidecar, never by the service: the call runs on the server the
	// operator bound because the service could not be reached.
	Degraded bool `json:"-"`
}

// Release gives a ticket back and reports how the call went, so the service can keep the
// performance history it keeps today. The service ignores fields it does not know.
type Release struct {
	Ticket           string `json:"ticket"`
	OK               bool   `json:"ok"`
	PromptTokens     int64  `json:"promptTokens,omitempty"`
	CompletionTokens int64  `json:"completionTokens,omitempty"`
	DurationMs       int64  `json:"durationMs,omitempty"`
}

// PlanRequest asks the service to plan an agent's first call when the coordinator selects it, so
// a model can load while triage runs. The service holds the plan for the thread.
type PlanRequest struct {
	Namespace string   `json:"namespace,omitempty"`
	Crew      string   `json:"crew"`
	Agent     string   `json:"agent"`
	ThreadID  string   `json:"threadId"`
	Phase     string   `json:"phase,omitempty"`
	Models    []string `json:"models"`
	MayUnload bool     `json:"mayUnload"`
}

// ErrNoFit means no server has room for the call; Kind (one of the Kind constants, or empty
// when the service gave none) says why.
type ErrNoFit struct{ Kind, Reason string }

func (e *ErrNoFit) Error() string { return "kubemoot_no_fit: " + e.Reason }

// ErrUnavailable means the service could not be reached or failed; the call can run degraded.
type ErrUnavailable struct{ Err error }

func (e *ErrUnavailable) Error() string { return "placement service unavailable: " + e.Err.Error() }

// Unwrap exposes the cause.
func (e *ErrUnavailable) Unwrap() error { return e.Err }

// Placer is the seam the model door depends on. onQueued, when not nil, is called once with the
// reason when the service says the call must wait for capacity.
type Placer interface {
	Place(ctx context.Context, r Request, onQueued func(reason string)) (Reply, error)
	Release(ctx context.Context, r Release) error
}

// Planner is implemented by a placer that can plan ahead of the first call.
type Planner interface {
	Plan(ctx context.Context, r PlanRequest) error
}

// Endpoint is one model on one server, the floor the operator bound.
type Endpoint struct{ Server, Endpoint, Model string }

// Bound places every call on the server the operator bound for the alias: no service call, no
// ticket. It is the placer when the contract names no service.
type Bound struct {
	// Aliases maps an alias to its bound model; DefaultAlias is the one for an unknown alias.
	Aliases map[string]Endpoint
	Default string
}

func (b Bound) endpoint(alias string) (Endpoint, bool) {
	if e, ok := b.Aliases[alias]; ok {
		return e, true
	}
	e, ok := b.Aliases[b.Default]
	return e, ok
}

// Place implements Placer.
func (b Bound) Place(_ context.Context, r Request, _ func(string)) (Reply, error) {
	e, ok := b.endpoint(r.Alias)
	if !ok {
		return Reply{}, fmt.Errorf("no bound server for alias %q", r.Alias)
	}
	return Reply{Server: e.Server, Endpoint: e.Endpoint, Model: e.Model, Reason: "bound", Evicted: []string{}}, nil
}

// Release implements Placer; a bound call holds no ticket.
func (Bound) Release(context.Context, Release) error { return nil }

// Fallback tries the service and, when it is unavailable, runs the call on the bound server and
// marks the reply degraded. A no-fit answer is the service's decision and is never overridden.
type Fallback struct {
	Primary Placer
	Bound   Bound
}

// Place implements Placer.
func (f Fallback) Place(ctx context.Context, r Request, onQueued func(string)) (Reply, error) {
	reply, err := f.Primary.Place(ctx, r, onQueued)
	var unavailable *ErrUnavailable
	if err == nil || !errors.As(err, &unavailable) {
		return reply, err
	}
	bound, berr := f.Bound.Place(ctx, r, nil)
	if berr != nil {
		return Reply{}, errors.Join(err, berr)
	}
	bound.Degraded = true
	bound.Reason = "bound: " + unavailable.Err.Error()
	return bound, nil
}

// Release implements Placer. A reply without a ticket (a degraded call) has nothing to release.
func (f Fallback) Release(ctx context.Context, r Release) error {
	if r.Ticket == "" {
		return nil
	}
	return f.Primary.Release(ctx, r)
}

// Plan implements Planner by planning on the service when it can.
func (f Fallback) Plan(ctx context.Context, r PlanRequest) error {
	if p, ok := f.Primary.(Planner); ok {
		return p.Plan(ctx, r)
	}
	return nil
}

// Plan implements Planner; a bound agent has nothing to plan.
func (Bound) Plan(context.Context, PlanRequest) error { return nil }
