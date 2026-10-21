// Package turn holds what the sidecar measures about one discussion turn from the calls that
// pass through its doors, so a harness never reports it and cannot get it wrong.
package turn

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Info is what the doors need to know about the turn a call belongs to.
type Info struct {
	ThreadID string
	// RequestDate (RFC 3339) and TimeZone (IANA) are the requester's, when the bus message
	// carried them. The tools door passes them to tools; empty means the requester sent none.
	RequestDate string
	TimeZone    string
	// OnWaiting is called once, with the reason, when the turn starts waiting for capacity or
	// runs without the placement service.
	OnWaiting func(reason string)
}

// Meter accumulates the measured facts of one turn. Safe for concurrent calls.
type Meter struct {
	info Info
	reg  *Registry

	mu           sync.Mutex
	inputTokens  int64
	outputTokens int64
	inferenceMs  int64
	models       map[string]struct{}
	places       []string
	tools        map[string]struct{}
	failedTool   string
	failures     int
	lastServer   string
	lastReason   string
	noFitKind    string
	waited       bool
	degraded     bool
}

// NewMeter returns an empty meter for a turn.
func NewMeter(info Info) *Meter {
	return &Meter{info: info, models: map[string]struct{}{}, tools: map[string]struct{}{}}
}

// Info returns what is known about the turn.
func (m *Meter) Info() Info { return m.info }

// ModelCall records one finished model call.
func (m *Meter) ModelCall(server, model, reason string, in, out int64, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputTokens += in
	m.outputTokens += out
	m.inferenceMs += took.Milliseconds()
	m.models[model] = struct{}{}
	m.places = append(m.places, model+"@"+server)
	m.lastServer, m.lastReason = server, reason
	if m.reg != nil {
		m.reg.lastCall.Store(time.Now().UnixMilli())
	}
}

// ToolCall records one tool call; a failed call also counts as a failure and names the tool.
func (m *Meter) ToolCall(name string, failed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[name] = struct{}{}
	if failed {
		m.failures++
		m.failedTool = name
	}
}

// Waiting tells the turn's owner, once, that the turn waits for capacity or runs degraded.
func (m *Meter) Waiting(reason string) {
	m.mu.Lock()
	first := !m.waited
	m.waited = true
	m.mu.Unlock()
	if first && m.info.OnWaiting != nil {
		m.info.OnWaiting(reason)
	}
}

// Degraded records that a call ran on the operator-bound server because the placement service was
// unreachable, and tells the turn's owner.
func (m *Meter) Degraded(reason string) {
	m.mu.Lock()
	m.degraded = true
	m.mu.Unlock()
	m.Waiting(reason)
}

// NoFit records that no server could take a call, with the kind the placement service gave
// (model-too-large, gpu-busy or prompt-too-large).
func (m *Meter) NoFit(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind == "" {
		kind = "gpu-busy"
	}
	m.noFitKind = kind
}

// NoFitKind is the kind of the last call that no server could take, or empty.
func (m *Meter) NoFitKind() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.noFitKind
}

// Snapshot is the metadata fields the sidecar fills on a signal, named as the bus schema names them.
func (m *Meter) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	md := map[string]any{
		"inputTokens":  m.inputTokens,
		"outputTokens": m.outputTokens,
		"totalTokens":  m.inputTokens + m.outputTokens,
		"inferenceMs":  m.inferenceMs,
	}
	if names := sorted(m.models); len(names) > 0 {
		md["modelsUsed"] = m.places
		md["modelName"] = m.lastModel()
	}
	if names := sorted(m.tools); len(names) > 0 {
		md["toolsCalled"] = names
	}
	if m.failures > 0 {
		md["failedTool"] = m.failedTool
		md["failureCount"] = m.failures
	}
	if m.lastServer != "" {
		md["server"] = m.lastServer
		md["pickReason"] = m.pickReason()
	}
	return md
}

func (m *Meter) lastModel() string {
	last := m.places[len(m.places)-1]
	for i := len(last) - 1; i >= 0; i-- {
		if last[i] == '@' {
			return last[:i]
		}
	}
	return last
}

func (m *Meter) pickReason() string {
	if m.degraded {
		return "degraded: " + m.lastReason
	}
	return m.lastReason
}

func sorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Registry maps the per-turn key a harness sends as its OpenAI API key to the turn's meter.
type Registry struct {
	mu       sync.Mutex
	meters   map[string]*Meter
	lastCall atomic.Int64
}

// LastCall is when the last model call finished; the zero time means none yet.
func (r *Registry) LastCall() time.Time {
	ms := r.lastCall.Load()
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{meters: map[string]*Meter{}} }

// Open starts a turn under key and returns its meter.
func (r *Registry) Open(key string, info Info) *Meter {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := NewMeter(info)
	m.reg = r
	r.meters[key] = m
	return m
}

// Close ends the turn under key.
func (r *Registry) Close(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.meters, key)
}

// Lookup returns the meter for key; with an unknown key and exactly one open turn it returns that turn
// (the concurrency 1 rule), otherwise nil.
func (r *Registry) Lookup(key string) *Meter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.meters[key]; ok {
		return m
	}
	if len(r.meters) == 1 {
		for _, m := range r.meters {
			return m
		}
	}
	return nil
}

// Active is how many turns are open.
func (r *Registry) Active() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.meters)
}
