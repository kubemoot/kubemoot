/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
)

// ToolCalibrateProvider runs the calibration recipe on a provider in the background.
const ToolCalibrateProvider = "calibrate_provider"

// ToolObservedPerformance compares crew calls since the calibration with it.
const ToolObservedPerformance = "observed_performance"

// defaultTargets is how many of the smallest downloaded models a calibration
// measures when no Model binds any to the provider.
const defaultTargets = 3

// Jobs runs calibrations in the background, at most one per provider.
type Jobs struct {
	// Base is the context jobs run under: the server's lifetime, not a tool call's.
	Base context.Context

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// start runs prepare and then, in the background, job for provider, unless a job
// is already running for it (false, nil). A prepare error is returned and nothing runs.
func (j *Jobs) start(provider string, prepare func() error, job func(context.Context)) (bool, error) {
	if !j.claim(provider) {
		return false, nil
	}
	if err := prepare(); err != nil {
		j.release(provider)
		return false, err
	}
	j.wg.Add(1)
	base := j.Base
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer j.wg.Done()
		defer j.release(provider)
		job(base)
	}()
	return true, nil
}

func (j *Jobs) claim(provider string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running == nil {
		j.running = map[string]bool{}
	}
	if j.running[provider] {
		return false
	}
	j.running[provider] = true
	return true
}

func (j *Jobs) release(provider string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.running, provider)
}

// Running reports whether a job is running for provider.
func (j *Jobs) Running(provider string) bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running[provider]
}

// Wait blocks until every running job has ended.
func (j *Jobs) Wait() { j.wg.Wait() }

type calibrateArgs struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
}

// errNoJobs is a tool set built without a jobs runner.
var errNoJobs = errors.New("calibrate_provider is not available: no jobs runner")

// calibrateProvider records Calibrating on the provider, then runs the recipe in
// the background and returns at once with the models it will measure, or reports
// a calibration already running.
func (s *Set) calibrateProvider(ctx context.Context, raw json.RawMessage) (any, error) {
	if s.Jobs == nil {
		return nil, errNoJobs
	}
	var args calibrateArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	if err := required(fieldProvider, args.Provider); err != nil {
		return nil, err
	}
	p, err := s.Cluster.Provider(ctx, args.Provider)
	if err != nil {
		return nil, err
	}
	if err := calibratable(p); err != nil {
		return nil, err
	}
	targets, err := s.targets(ctx, p, args.Models)
	if err != nil {
		return nil, err
	}
	name := p.Name
	started, err := s.Jobs.start(name,
		func() error {
			_, err := s.record(ctx, name, PhaseCalibrating, "calibrating "+strings.Join(targets, ", "), nil)
			return err
		},
		func(jobCtx context.Context) { s.runRecipe(jobCtx, name, targets) })
	if err != nil {
		return nil, fmt.Errorf("starting the calibration: %w", err)
	}
	if !started {
		return map[string]any{fieldProvider: name, "running": true}, nil
	}
	return map[string]any{fieldProvider: name, "started": true, fieldModels: targets}, nil
}

// calibratable refuses a provider that is not ready or has calibration turned off.
func calibratable(p *aiv1alpha1.ModelProvider) error {
	if !p.Status.Ready {
		return fmt.Errorf("provider %s is not ready", p.Name)
	}
	if !cluster.CalibrationEnabled(p) {
		return fmt.Errorf("provider %s has spec.calibration.enabled false", p.Name)
	}
	return nil
}

// targets is the models to measure: those asked for, else the downloaded models
// that Models bind to the provider, else the smallest downloaded models that fit.
func (s *Set) targets(ctx context.Context, p *aiv1alpha1.ModelProvider, asked []string) ([]string, error) {
	if len(asked) > 0 {
		for _, m := range asked {
			if _, ok := sizeOf(p, m); !ok {
				return nil, fmt.Errorf("%s has not reported %s among its downloaded models", p.Name, m)
			}
		}
		return dedupe(asked), nil
	}
	bound, err := s.Cluster.BoundModels(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	var downloaded []string
	for _, m := range dedupe(bound) {
		if _, ok := sizeOf(p, m); ok {
			downloaded = append(downloaded, m)
		}
	}
	if len(downloaded) > 0 {
		return downloaded, nil
	}
	if smallest := smallestFitting(p, defaultTargets); len(smallest) > 0 {
		return smallest, nil
	}
	return nil, fmt.Errorf("%s has no downloaded model that fits its memory to calibrate", p.Name)
}

func dedupe(models []string) []string {
	var out []string
	for _, m := range models {
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// smallestFitting is up to n downloaded models, smallest first, that fit the
// provider's memory budget (any size when the budget is unknown).
func smallestFitting(p *aiv1alpha1.ModelProvider, n int) []string {
	models := available(p)
	slices.SortStableFunc(models, func(a, b availableModel) int { return cmp.Compare(a.SizeMiB, b.SizeMiB) })
	budget := cluster.UsableMemoryMiB(p)
	var out []string
	for _, m := range models {
		if len(out) == n {
			break
		}
		if budget == 0 || m.SizeMiB <= budget {
			out = append(out, m.Model)
		}
	}
	return out
}

// runRecipe measures each target in turn and records the outcome: Calibrated, or
// Waiting when crew calls took the provider or the server is stopping (with the
// results so far), or Failed when no model could be measured.
func (s *Set) runRecipe(ctx context.Context, provider string, targets []string) {
	var results []aiv1alpha1.ModelCalibration
	var failures []string
	for _, model := range targets {
		if ctx.Err() != nil {
			s.finishRecipe(ctx, provider, PhaseWaiting, interruptedMessage, results)
			return
		}
		result, err := s.measure(ctx, provider, model)
		if err != nil && ctx.Err() != nil {
			s.finishRecipe(ctx, provider, PhaseWaiting, interruptedMessage, results)
			return
		}
		if errors.Is(err, errBusy) {
			s.finishRecipe(ctx, provider, PhaseWaiting, "paused: crew calls are using the provider", results)
			return
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", model, err))
			continue
		}
		results = append(results, result)
	}
	if len(results) == 0 {
		s.finishRecipe(ctx, provider, PhaseFailed, "no model could be measured: "+strings.Join(failures, "; "), nil)
		return
	}
	message := fmt.Sprintf("measured %d of %d models", len(results), len(targets))
	if len(failures) > 0 {
		message += "; not measured: " + strings.Join(failures, "; ")
	}
	s.finishRecipe(ctx, provider, PhaseCalibrated, message, results)
}

// finishRecipe records how a run ended. It is written even while the server stops,
// so a run never stays Calibrating with no job behind it.
func (s *Set) finishRecipe(ctx context.Context, provider, phase, message string,
	results []aiv1alpha1.ModelCalibration) {
	if _, err := s.record(context.WithoutCancel(ctx), provider, phase, message, results); err != nil {
		log.Printf("calibration of %s ended %s but could not be recorded: %v", provider, phase, err)
	}
}

// errBusy is a probe that found crew calls holding the provider.
var errBusy = errors.New("crew calls are using the provider")

// measure runs the recipe on one model: load, two short requests, a long prompt,
// and, when the provider runs requests together, a parallel batch. Every number is
// copied from a probe: generation speed from the second (warm) short request,
// prompt speed from the long prompt.
func (s *Set) measure(ctx context.Context, provider, model string) (aiv1alpha1.ModelCalibration, error) {
	p, err := s.Cluster.Provider(ctx, provider)
	if err != nil {
		return aiv1alpha1.ModelCalibration{}, err
	}
	run := func(kind string) (probe.Result, error) { return s.probeOnce(ctx, p, model, kind) }
	load, err := run(probe.KindLoad)
	if err != nil {
		return aiv1alpha1.ModelCalibration{}, err
	}
	result := aiv1alpha1.ModelCalibration{Model: model, Fits: load.Fits, Reason: load.Reason, LoadMs: load.LoadMs}
	if !load.Fits {
		return result, nil
	}
	fillResident(&result, load.Resident, cluster.HasAccelerator(p))
	steps := []string{probe.KindShort, probe.KindShort, probe.KindLong}
	if cluster.MaxParallel(p) > 1 {
		steps = append(steps, probe.KindParallel)
	}
	for _, kind := range steps {
		r, err := run(kind)
		if err != nil {
			return aiv1alpha1.ModelCalibration{}, err
		}
		fillStep(&result, kind, r)
	}
	return result, nil
}

// interruptedMessage explains a run the server stopped before it ended.
const interruptedMessage = "interrupted: the calibration server stopped; it resumes when the provider is idle"

// RecoverInterrupted records Waiting on every provider left Calibrating, since
// this server runs every calibration job and none runs at startup; the operator
// resumes them when the provider is idle. Call it before serving.
func (s *Set) RecoverInterrupted(ctx context.Context) error {
	providers, err := s.Cluster.Providers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range providers {
		if p.Status.Calibration == nil || p.Status.Calibration.Phase != PhaseCalibrating {
			continue
		}
		if _, err := s.record(ctx, p.Name, PhaseWaiting, interruptedMessage, nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// requestFor is a probe of model on the provider, with the provider's limits.
func requestFor(p *aiv1alpha1.ModelProvider, model, kind string) probe.Request {
	size, _ := sizeOf(p, model)
	return probe.Request{
		Provider: p.Name, Endpoint: p.Spec.Endpoint, Model: model, Kind: kind,
		MaxParallel: cluster.MaxParallel(p), SizeMiB: size,
		MemoryMiB: cluster.UsableMemoryMiB(p), Accelerator: cluster.HasAccelerator(p),
	}
}

// probeOnce sends one probe; a busy provider is errBusy.
func (s *Set) probeOnce(ctx context.Context, p *aiv1alpha1.ModelProvider, model, kind string) (probe.Result, error) {
	r, err := s.Prober.Probe(ctx, requestFor(p, model, kind))
	if err == nil && r.Busy {
		err = errBusy
	}
	return r, err
}

func fillResident(result *aiv1alpha1.ModelCalibration, resident *probe.Resident, accelerator bool) {
	if resident == nil {
		return
	}
	result.ContextLength = resident.ContextLength
	result.ResidentMiB = resident.SizeMiB
	if accelerator {
		result.ResidentMiB = resident.VRAMMiB
	}
}

func fillStep(result *aiv1alpha1.ModelCalibration, kind string, r probe.Result) {
	switch kind {
	case probe.KindShort:
		result.GenerateTokensPerSecond = r.GenerateTokensPerSecond
	case probe.KindLong:
		result.PromptTokensPerSecond = r.PromptTokensPerSecond
	case probe.KindParallel:
		result.ParallelRequests = r.ParallelRequests
		result.ParallelGenerateTokensPerSecond = r.ParallelGenerateTokensPerSecond
	}
}

// observedPerformance shows, per model, how fast crew calls generated on the
// provider since its calibration, alone and alongside other calls, beside the
// calibrated speed, and which models drifted by the operator's rule.
func (s *Set) observedPerformance(ctx context.Context, raw json.RawMessage) (any, error) {
	var args providerArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	if err := required(fieldProvider, args.Provider); err != nil {
		return nil, err
	}
	p, err := s.Cluster.Provider(ctx, args.Provider)
	if err != nil {
		return nil, err
	}
	entries, err := s.History.Of(p.Name)
	if err != nil {
		return nil, fmt.Errorf("reading the performance history: %w", err)
	}
	others, err := s.Tickets.Others(p.Name)
	if err != nil {
		return nil, fmt.Errorf("reading the provider's tickets: %w", err)
	}
	calibrated, since := perf.CalibratedSpeeds(p.Status.Calibration)
	observed := perf.Observe(entries, p.Name, since)
	return map[string]any{
		fieldProvider: p.Name,
		"calibration": p.Status.Calibration,
		"callsNow":    others,
		"observed":    observed,
		"drifted":     perf.Drifted(calibrated, observed),
		"driftRule": fmt.Sprintf("median speed of warm crew calls running alone below %.0f%% or above %.0f%% "+
			"of the calibrated speed, over at least %d calls since the calibration",
			perf.MinRatio*100, perf.MaxRatio*100, perf.MinSamples),
	}, nil
}
