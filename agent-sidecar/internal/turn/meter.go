// Package turn holds what the sidecar measures about one discussion turn from the calls that
// pass through its doors, so a harness never reports it and cannot get it wrong.
package turn

import (
	"sort"
	"sync"
	"time"
)

// Meter accumulates the measured facts of one turn. Safe for concurrent calls.
type Meter struct {
	mu           sync.Mutex
	inputTokens  int64
	outputTokens int64
	inferenceMs  int64
	models       map[string]struct{}
	tools        map[string]struct{}
	failedTool   string
	failures     int
	lastServer   string
	lastReason   string
}

// NewMeter returns an empty meter.
func NewMeter() *Meter {
	return &Meter{models: map[string]struct{}{}, tools: map[string]struct{}{}}
}

// ModelCall records one finished model call.
func (m *Meter) ModelCall(server, model, reason string, in, out int64, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputTokens += in
	m.outputTokens += out
	m.inferenceMs += took.Milliseconds()
	m.models[model] = struct{}{}
	m.lastServer, m.lastReason = server, reason
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
		md["modelsUsed"] = names
		md["modelName"] = names[len(names)-1]
	}
	if names := sorted(m.tools); len(names) > 0 {
		md["toolsUsed"] = names
	}
	if m.failures > 0 {
		md["failedTool"] = m.failedTool
		md["failureCount"] = m.failures
	}
	if m.lastServer != "" {
		md["server"] = m.lastServer
		md["pickReason"] = m.lastReason
	}
	return md
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
	mu     sync.Mutex
	meters map[string]*Meter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{meters: map[string]*Meter{}} }

// Open starts a turn under key and returns its meter.
func (r *Registry) Open(key string) *Meter {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := NewMeter()
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
