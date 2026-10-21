/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package probe sends the calibration probes the calibrator agent asks for, one
// probe per call, and enforces the rules that keep calibration out of a crew's way:
// it refuses while other calls hold tickets on the server, claims tickets for its
// own requests like any caller, never loads a model larger than the server's
// budget, and remembers the models it loaded so Finish can unload them. What to
// probe, in what order, and when the results are good enough is the agent's call.
package probe

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
)

// The probes an agent can ask for.
const (
	KindLoad     = "load"
	KindShort    = "short"
	KindLong     = "long"
	KindParallel = "parallel"
)

// Kinds lists the probes in the order a calibration usually runs them.
var Kinds = []string{KindLoad, KindShort, KindLong, KindParallel}

// Prompts are the requests the probes send.
type Prompts struct {
	Short          string
	Long           string
	ShortTokens    int
	LongTokens     int
	ScratchMiB     int64
	SpillTolerance float64
}

// DefaultPrompts is a short question and about 1,500 tokens of plain text, so the
// long probe's time is mostly prompt processing.
func DefaultPrompts() Prompts {
	sentence := "A cluster runs many workloads, and each one asks for memory, compute, " +
		"and time on the machines that host it. "
	return Prompts{
		Short:          "In one sentence, what does a scheduler do?",
		Long:           "Read the notes below, then answer with one word: ready.\n\n" + strings.Repeat(sentence, 70),
		ShortTokens:    48,
		LongTokens:     8,
		ScratchMiB:     512,
		SpillTolerance: 0.95,
	}
}

// Request is one probe on one model of one server.
type Request struct {
	Server      string
	Endpoint    string
	Model       string
	Kind        string
	MaxParallel int
	// SizeMiB is the model's size on disk; 0 when unknown.
	SizeMiB int64
	// MemoryMiB is the budget a model must fit; 0 when unknown.
	MemoryMiB int64
	// Accelerator is true when a model only partly in accelerator memory has spilled.
	Accelerator bool
	// EngineType is the server's engine (spec.type); empty is Ollama. Prober.For picks
	// the engine client by it.
	EngineType string
}

// Result is what one probe measured. Times are milliseconds, speeds tokens per second.
type Result struct {
	Probe                           string    `json:"probe"`
	Server                          string    `json:"server"`
	Model                           string    `json:"model"`
	Busy                            bool      `json:"busy,omitempty"`
	Fits                            bool      `json:"fits"`
	Reason                          string    `json:"reason,omitempty"`
	LoadMs                          int64     `json:"loadMs,omitempty"`
	PromptTokens                    int64     `json:"promptTokens,omitempty"`
	PromptMs                        int64     `json:"promptMs,omitempty"`
	OutputTokens                    int64     `json:"outputTokens,omitempty"`
	GenerateMs                      int64     `json:"generateMs,omitempty"`
	PromptTokensPerSecond           int64     `json:"promptTokensPerSecond,omitempty"`
	GenerateTokensPerSecond         int64     `json:"generateTokensPerSecond,omitempty"`
	ParallelRequests                int       `json:"parallelRequests,omitempty"`
	ParallelGenerateTokensPerSecond int64     `json:"parallelGenerateTokensPerSecond,omitempty"`
	Resident                        *Resident `json:"resident,omitempty"`
}

// Resident is the probed model as the engine holds it after the probe.
type Resident struct {
	SizeMiB       int64 `json:"sizeMiB"`
	VRAMMiB       int64 `json:"acceleratorMiB"`
	ContextLength int   `json:"contextLength"`
}

// ErrUnknownProbe is returned for a probe kind not in Kinds.
var ErrUnknownProbe = errors.New("unknown probe; use load, short, long, or parallel")

// Prober runs probes and tracks the models it loaded on each server. Probe,
// Finish, and FinishIdle run one at a time, so a model is never unloaded under a
// probe that is using it.
type Prober struct {
	Engine func(endpoint string) engine.Engine
	// For picks the engine client for a request when the engine is not Ollama (a vLLM
	// server is per model and needs the server and model to ask the operator to wake
	// it). Nil uses Engine(endpoint).
	For     func(Request) engine.Engine
	Tickets store.Tickets
	History store.History
	Prompts Prompts
	Now     func() time.Time

	op     sync.Mutex // serializes Probe, Finish, and FinishIdle
	loaded map[string]map[string]loadedModel
}

type loadedModel struct {
	engine   engine.Engine
	endpoint string
	lastUsed time.Time
}

// Probe runs one probe. A server with other calls on it gets Busy and nothing is sent.
func (p *Prober) Probe(ctx context.Context, req Request) (Result, error) {
	p.op.Lock()
	defer p.op.Unlock()
	result := Result{Probe: req.Kind, Server: req.Server, Model: req.Model, Fits: true}
	if refused, err := p.refuse(req, &result); refused || err != nil {
		return result, err
	}
	eng := p.engineFor(req)
	wasLoaded, err := p.isLoaded(ctx, eng, req.Model)
	if err != nil {
		return result, err
	}
	return result, p.probe(ctx, eng, req, wasLoaded, &result)
}

// EmbeddingOnly reports whether the engine says the model only embeds, so it has
// nothing to calibrate. An engine that cannot say reports false.
func (p *Prober) EmbeddingOnly(ctx context.Context, req Request) (bool, error) {
	checker, ok := p.engineFor(req).(engine.EmbeddingChecker)
	if !ok {
		return false, nil
	}
	return checker.EmbeddingOnly(ctx, req.Model)
}

// refuse reports a probe that must not be sent: an unknown kind, a model larger
// than the budget, or a server other calls are using.
func (p *Prober) refuse(req Request, result *Result) (bool, error) {
	if !validKind(req.Kind) {
		return true, ErrUnknownProbe
	}
	if req.MemoryMiB > 0 && req.SizeMiB > req.MemoryMiB {
		result.Fits = false
		result.Reason = fmt.Sprintf("%d MiB on disk is more than the server's %d MiB budget",
			req.SizeMiB, req.MemoryMiB)
		return true, nil
	}
	busy, err := p.busy(req.Server)
	result.Busy = busy
	return busy, err
}

// probe remembers a model it is about to load, runs the probe, and checks the
// server is still free of other calls.
func (p *Prober) probe(ctx context.Context, eng engine.Engine, req Request, wasLoaded bool, result *Result) error {
	_, remembered := p.loaded[req.Server][req.Model]
	p.touch(req, eng, !wasLoaded)
	if err := p.run(ctx, eng, req, wasLoaded, result); err != nil || result.Busy {
		if result.Busy && !wasLoaded && !remembered {
			delete(p.loaded[req.Server], req.Model) // nothing was sent, so nothing was loaded
		}
		return err
	}
	busy, err := p.busy(req.Server)
	result.Busy = busy
	return err
}

func validKind(kind string) bool {
	for _, k := range Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func (p *Prober) busy(server string) (bool, error) {
	others, err := p.Tickets.Others(server)
	if err != nil {
		return false, fmt.Errorf("reading tickets: %w", err)
	}
	return others > 0, nil
}

func (p *Prober) isLoaded(ctx context.Context, eng engine.Engine, model string) (bool, error) {
	residents, err := eng.Loaded(ctx)
	if err != nil {
		return false, fmt.Errorf("reading loaded models: %w", err)
	}
	return residentOf(residents, model) != nil, nil
}

// touch remembers a model this probe is about to load, before it sends anything, so
// a load that succeeds after a failed reply is still unloaded later; and refreshes
// the idle clock of a model it loaded earlier.
func (p *Prober) touch(req Request, eng engine.Engine, loading bool) {
	models := p.loaded[req.Server]
	if _, remembered := models[req.Model]; !remembered && !loading {
		return
	}
	if p.loaded == nil {
		p.loaded = map[string]map[string]loadedModel{}
	}
	if models == nil {
		models = map[string]loadedModel{}
		p.loaded[req.Server] = models
	}
	models[req.Model] = loadedModel{engine: eng, endpoint: req.Endpoint, lastUsed: p.Now()}
}

// run claims the probe's tickets, checks no one else arrived (and sends nothing,
// with result.Busy set, when someone did), sends its requests, records them, and
// releases the tickets.
func (p *Prober) run(ctx context.Context, eng engine.Engine, req Request, wasLoaded bool, result *Result) error {
	n := 1
	if req.Kind == KindParallel {
		n = max(1, req.MaxParallel)
	}
	footprint := p.Prompts.ScratchMiB
	if !wasLoaded && req.SizeMiB > 0 {
		footprint = req.SizeMiB
	}
	keys, err := p.claim(req, footprint, n)
	defer p.release(keys)
	if err != nil {
		return err
	}
	if busy, err := p.busy(req.Server); err != nil || busy {
		result.Busy = busy
		return err
	}
	replies, errs, samples := p.send(ctx, eng, req, n, !wasLoaded)
	if err := p.History.Record(req.Server, req.Model, samples); err != nil {
		log.Printf("recording calibration samples for %s on %s: %v", req.Model, req.Server, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	fill(result, req, replies, wasLoaded)
	return p.checkResidency(ctx, eng, req, result)
}

func (p *Prober) claim(req Request, firstFootprint int64, n int) ([]string, error) {
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		footprint := p.Prompts.ScratchMiB
		if i == 0 {
			footprint = firstFootprint
		}
		key, err := p.Tickets.Claim(req.Server, req.Model, footprint)
		if err != nil {
			return keys, fmt.Errorf("claiming a ticket: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (p *Prober) release(keys []string) {
	for _, key := range keys {
		p.Tickets.Release(key)
	}
}

// send sends n requests together and returns them with their samples.
func (p *Prober) send(ctx context.Context, eng engine.Engine, req Request, n int,
	cold bool) ([]engine.Reply, []error, []store.Sample) {
	prompt, tokens := p.Prompts.Short, p.Prompts.ShortTokens
	switch req.Kind {
	case KindLong:
		prompt, tokens = p.Prompts.Long, p.Prompts.LongTokens
	case KindLoad:
		tokens = 1
	}
	replies := make([]engine.Reply, n)
	errs := make([]error, n)
	start := p.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			replies[i], errs[i] = eng.Generate(ctx, req.Model, prompt, tokens)
		}(i)
	}
	wg.Wait()
	end := p.Now()
	samples := make([]store.Sample, 0, n)
	for i := range replies {
		samples = append(samples, sampleOf(start, end, replies[i], errs[i] == nil, cold && i == 0, n))
	}
	return replies, errs, samples
}

// fill sets the result's timings and speeds from the replies.
func fill(result *Result, req Request, replies []engine.Reply, wasLoaded bool) {
	first := replies[0]
	if !wasLoaded {
		result.LoadMs = first.LoadMs
	}
	result.PromptTokens, result.PromptMs = first.PromptTokens, first.PromptMs
	result.OutputTokens, result.GenerateMs = first.OutputTokens, first.GenerateMs
	result.PromptTokensPerSecond = perSecond(first.PromptTokens, first.PromptMs)
	result.GenerateTokensPerSecond = perSecond(first.OutputTokens, first.GenerateMs)
	if req.Kind != KindParallel {
		return
	}
	var tokens, longestMs int64
	for _, r := range replies {
		tokens += r.OutputTokens
		longestMs = max(longestMs, r.GenerateMs)
	}
	result.ParallelRequests = len(replies)
	result.ParallelGenerateTokensPerSecond = perSecond(tokens, longestMs)
}

// checkResidency reports how the engine holds the model now, and a spill.
func (p *Prober) checkResidency(ctx context.Context, eng engine.Engine, req Request, result *Result) error {
	residents, err := eng.Loaded(ctx)
	if err != nil {
		return fmt.Errorf("reading loaded models: %w", err)
	}
	r := residentOf(residents, req.Model)
	if r == nil {
		return nil
	}
	result.Resident = &Resident{SizeMiB: r.SizeMiB, VRAMMiB: r.VRAMMiB, ContextLength: r.ContextLength}
	if req.Accelerator && r.SizeMiB > 0 && float64(r.VRAMMiB) < p.Prompts.SpillTolerance*float64(r.SizeMiB) {
		result.Fits = false
		result.Reason = fmt.Sprintf("spills to the CPU: %d of %d MiB on the accelerator", r.VRAMMiB, r.SizeMiB)
	}
	return nil
}

// Finish unloads the models this server loaded on the server, except one a crew
// call is using now, and returns those it unloaded, sorted, with an error naming
// any that failed. A model in use or one that failed to unload stays remembered
// for a later Finish. When the tickets cannot be read it unloads nothing.
func (p *Prober) Finish(ctx context.Context, server string) ([]string, error) {
	p.op.Lock()
	defer p.op.Unlock()
	return p.finish(ctx, server, func(loadedModel) bool { return true })
}

// FinishIdle unloads, on every server, the models this server loaded and has not
// probed for idle: the safety net when an agent never finishes a calibration.
func (p *Prober) FinishIdle(ctx context.Context, idle time.Duration) {
	p.op.Lock()
	defer p.op.Unlock()
	stale := func(m loadedModel) bool { return p.Now().Sub(m.lastUsed) > idle }
	for server := range p.loaded {
		if _, err := p.finish(ctx, server, stale); err != nil {
			log.Printf("unloading idle calibration models on %s: %v", server, err)
		}
	}
}

// finish unloads the server's remembered models that pick selects.
func (p *Prober) finish(ctx context.Context, server string, pick func(loadedModel) bool) ([]string, error) {
	models := p.loaded[server]
	if len(models) == 0 {
		return nil, nil
	}
	inUse, err := p.Tickets.ModelsInUse(server)
	if err != nil {
		return nil, fmt.Errorf("reading tickets: %w", err)
	}
	var unloaded []string
	var failed []error
	for model, m := range models {
		if !pick(m) || inUse[model] {
			continue
		}
		if err := m.client(p).Unload(ctx, model); err != nil {
			failed = append(failed, fmt.Errorf("unloading %s: %w", model, err))
			continue
		}
		delete(models, model)
		unloaded = append(unloaded, model)
	}
	if len(models) == 0 {
		delete(p.loaded, server)
	}
	sort.Strings(unloaded)
	return unloaded, errors.Join(failed...)
}

// engineFor is the engine client for a request.
func (p *Prober) engineFor(req Request) engine.Engine {
	if p.For != nil && req.EngineType != "" {
		return p.For(req)
	}
	return p.Engine(req.Endpoint)
}

// client is the engine client the model was loaded through.
func (m loadedModel) client(p *Prober) engine.Engine {
	if m.engine != nil {
		return m.engine
	}
	return p.Engine(m.endpoint)
}

func residentOf(residents []engine.Resident, model string) *engine.Resident {
	for i := range residents {
		if residents[i].Model == model {
			return &residents[i]
		}
	}
	return nil
}

// sampleOf is one request in the performance history; a failed request has no
// engine timings, which the history marks as store.Unknown.
func sampleOf(start, end time.Time, reply engine.Reply, ok, cold bool, active int) store.Sample {
	s := store.Sample{
		At: start.UnixMilli(), Kind: store.KindCalibration, Ok: ok, ColdStart: cold,
		PromptTokens: reply.PromptTokens, OutputTokens: reply.OutputTokens,
		DurationMs: end.Sub(start).Milliseconds(),
		LoadMs:     reply.LoadMs, PromptEvalMs: reply.PromptMs, EvalMs: reply.GenerateMs,
		ActiveCalls: int64(active),
	}
	if !ok {
		s.LoadMs, s.PromptEvalMs, s.EvalMs = store.Unknown, store.Unknown, store.Unknown
	}
	return s
}

func perSecond(tokens, ms int64) int64 {
	if tokens <= 0 || ms <= 0 {
		return 0
	}
	return tokens * 1000 / ms
}
