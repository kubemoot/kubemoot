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
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
)

// ToolCalibrateServer runs the calibration recipe on a server in the background.
const ToolCalibrateServer = "calibrate_server"

// ToolObservedPerformance compares crew calls since the calibration with it.
const ToolObservedPerformance = "observed_performance"

// defaultTargets is how many of the smallest downloaded models a calibration
// measures when no Model binds any to the server.
const defaultTargets = 3

// Jobs runs calibrations in the background, at most one per server.
type Jobs struct {
	// Base is the context jobs run under: the server's lifetime, not a tool call's.
	Base context.Context

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// start runs prepare and then, in the background, job for server, unless a job
// is already running for it (false, nil). A prepare error is returned and nothing runs.
func (j *Jobs) start(server string, prepare func() error, job func(context.Context)) (bool, error) {
	if !j.claim(server) {
		return false, nil
	}
	if err := prepare(); err != nil {
		j.release(server)
		return false, err
	}
	j.wg.Add(1)
	base := j.Base
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer j.wg.Done()
		defer j.release(server)
		job(base)
	}()
	return true, nil
}

func (j *Jobs) claim(server string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running == nil {
		j.running = map[string]bool{}
	}
	if j.running[server] {
		return false
	}
	j.running[server] = true
	return true
}

func (j *Jobs) release(server string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.running, server)
}

// Running reports whether a job is running for server.
func (j *Jobs) Running(server string) bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running[server]
}

// Wait blocks until every running job has ended.
func (j *Jobs) Wait() { j.wg.Wait() }

type calibrateArgs struct {
	Server string   `json:"server"`
	Models []string `json:"models"`
}

// errNoJobs is a tool set built without a jobs runner.
var errNoJobs = errors.New("calibrate_server is not available: no jobs runner")

// calibrateServer records Calibrating on the server, then runs the recipe in
// the background and returns at once with the models it will measure, or reports
// a calibration already running.
func (s *Set) calibrateServer(ctx context.Context, raw json.RawMessage) (any, error) {
	if s.Jobs == nil {
		return nil, errNoJobs
	}
	var args calibrateArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	if err := required(fieldServer, args.Server); err != nil {
		return nil, err
	}
	p, err := s.Cluster.Server(ctx, args.Server)
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
			_, err := s.record(ctx, name, PhaseCalibrating, "", "calibrating "+strings.Join(targets, ", "), nil)
			return err
		},
		func(jobCtx context.Context) { s.runRecipe(jobCtx, name, targets) })
	if err != nil {
		return nil, fmt.Errorf("starting the calibration: %w", err)
	}
	if !started {
		return map[string]any{fieldServer: name, "running": true}, nil
	}
	return map[string]any{fieldServer: name, "started": true, fieldModels: targets}, nil
}

// calibratable refuses a server that is not ready or has calibration turned off.
func calibratable(p *aiv1alpha1.ModelServer) error {
	if !p.Status.Ready {
		return fmt.Errorf("server %s is not ready", p.Name)
	}
	if !cluster.CalibrationEnabled(p) {
		return fmt.Errorf("server %s has spec.calibration.enabled false", p.Name)
	}
	return nil
}

// targets is the models to measure: those asked for, else the downloaded models
// that Models bind to the server, else the smallest downloaded models that fit.
func (s *Set) targets(ctx context.Context, p *aiv1alpha1.ModelServer, asked []string) ([]string, error) {
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
	for _, m := range dedupe(append(bound, presentCatalog(p)...)) {
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

// presentCatalog is the models the server's catalog says it has, so a model no Model
// object names is measured too, as the operator counts it when it decides a
// calibration is needed. Embedding models are not in the catalog; measure skips them.
func presentCatalog(p *aiv1alpha1.ModelServer) []string {
	return p.PresentCatalogModels()
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
// server's memory budget (any size when the budget is unknown).
func smallestFitting(p *aiv1alpha1.ModelServer, n int) []string {
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

// runRecipe measures each target in turn, each on its own: one model's failure never
// stops the others. It records a result per model (Measured, Skipped as not
// applicable, or Failed with its reason) and the outcome: Calibrated when every model
// that chats was measured, Failed when one genuinely could not be, or Waiting when
// crew calls took the server, the server is stopping, or the bus is not ready (NATS
// unreachable, a scheduler bucket not created yet; reason WaitingForBus, never
// Failed: nothing was learned about the models), with the results so far.
func (s *Set) runRecipe(ctx context.Context, server string, targets []string) {
	var results []aiv1alpha1.ModelCalibration
	for _, model := range targets {
		result, err := s.measureUnlessStopped(ctx, server, model)
		if reason, message, paused := pauseOf(ctx, err); paused {
			s.finishRecipe(ctx, server, PhaseWaiting, reason, message, results)
			return
		}
		if err != nil {
			result = aiv1alpha1.ModelCalibration{Model: model, Fits: true,
				Status: aiv1alpha1.ModelCalibrationFailed, Reason: err.Error()}
		}
		results = append(results, result)
	}
	phase, message := concludeRecipe(results)
	s.finishRecipe(ctx, server, phase, "", message, results)
}

// measureUnlessStopped measures the model, or reports the stop when the server is
// already stopping.
func (s *Set) measureUnlessStopped(ctx context.Context, server, model string) (aiv1alpha1.ModelCalibration, error) {
	if err := ctx.Err(); err != nil {
		return aiv1alpha1.ModelCalibration{}, err
	}
	return s.measure(ctx, server, model)
}

// pauseOf says whether a run must pause (as Waiting) instead of going on, with the
// wait reason and message: the server is stopping, crew calls hold the server, or
// the bus is not ready.
func pauseOf(ctx context.Context, err error) (reason, message string, paused bool) {
	switch {
	case err != nil && ctx.Err() != nil:
		return "", interruptedMessage, true
	case errors.Is(err, errBusy):
		return "", "paused: crew calls are using the server", true
	case errors.Is(err, bus.ErrNotReady):
		return aiv1alpha1.CalibrationReasonWaitingForBus, busNotReadyMessage(err), true
	}
	return "", "", false
}

// concludeRecipe is the phase and message of a finished run from its per-model
// results: Failed when any model that chats could not be measured, else Calibrated
// (also when every model was skipped and there was nothing to measure).
func concludeRecipe(results []aiv1alpha1.ModelCalibration) (phase, message string) {
	var measured int
	var failed, skipped []string
	for _, r := range results {
		switch r.Status {
		case aiv1alpha1.ModelCalibrationFailed:
			failed = append(failed, r.Model+": "+r.Reason)
		case aiv1alpha1.ModelCalibrationSkipped:
			skipped = append(skipped, r.Model)
		default:
			measured++
		}
	}
	note := ""
	if len(skipped) > 0 {
		note = "; skipped, not applicable: " + strings.Join(skipped, ", ")
	}
	if len(failed) > 0 {
		return PhaseFailed, fmt.Sprintf("could not measure %d of %d models that chat (%d measured): %s%s",
			len(failed), len(failed)+measured, measured, strings.Join(failed, "; "), note)
	}
	if measured == 0 {
		return PhaseCalibrated, "no model that chats to measure" + note
	}
	return PhaseCalibrated, fmt.Sprintf("measured %d of %d models that chat%s", measured, measured, note)
}

// finishRecipe records how a run ended. It is written even while the server stops,
// so a run never stays Calibrating with no job behind it.
func (s *Set) finishRecipe(ctx context.Context, server, phase, reason, message string,
	results []aiv1alpha1.ModelCalibration) {
	if _, err := s.record(context.WithoutCancel(ctx), server, phase, reason, message, results); err != nil {
		log.Printf("calibration of %s ended %s but could not be recorded: %v", server, phase, err)
	}
}

// busNotReadyMessage explains a run paused because the scheduler's buckets are not
// there yet or NATS cannot be reached; the run resumes when the bus is ready.
func busNotReadyMessage(err error) string {
	return "waiting for the bus: " + err.Error() +
		"; the calibration resumes when NATS and its scheduler buckets are ready"
}

// errBusy is a probe that found crew calls holding the server.
var errBusy = errors.New("crew calls are using the server")

// measure runs the recipe on one model: load, two short requests, a long prompt,
// and, when the server runs requests together, a parallel batch. Every number is
// copied from a probe: generation speed from the second (warm) short request,
// prompt speed from the long prompt.
func (s *Set) measure(ctx context.Context, server, model string) (aiv1alpha1.ModelCalibration, error) {
	p, err := s.Cluster.Server(ctx, server)
	if err != nil {
		return aiv1alpha1.ModelCalibration{}, err
	}
	only, checkErr := s.Prober.EmbeddingOnly(ctx, requestFor(p, model, probe.KindLoad))
	if checkErr == nil && only {
		return aiv1alpha1.ModelCalibration{Model: model, Fits: true, Status: aiv1alpha1.ModelCalibrationSkipped,
			Reason: embeddingSkipReason}, nil
	}
	result, err := s.measureChat(ctx, p, model)
	if err != nil && checkErr != nil {
		err = fmt.Errorf("%w (could not check whether it is an embedding model: %v)", err, checkErr)
	}
	return result, err
}

// measureChat runs the recipe on a model that is taken to chat.
func (s *Set) measureChat(ctx context.Context, p *aiv1alpha1.ModelServer,
	model string) (aiv1alpha1.ModelCalibration, error) {
	run := func(kind string) (probe.Result, error) { return s.probeOnce(ctx, p, model, kind) }
	load, err := run(probe.KindLoad)
	if err != nil {
		return aiv1alpha1.ModelCalibration{}, err
	}
	result := aiv1alpha1.ModelCalibration{Model: model, Fits: load.Fits, Reason: load.Reason, LoadMs: load.LoadMs,
		Status: aiv1alpha1.ModelCalibrationMeasured}
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

// embeddingSkipReason is why calibration skips an embedding model: it cannot be asked
// to generate, so there is no generation speed to measure and nothing is wrong.
const embeddingSkipReason = "embedding model: generation speed does not apply"

// interruptedMessage explains a run the server stopped before it ended.
const interruptedMessage = "interrupted: the calibration server stopped; it resumes when the server is idle"

// RecoverInterrupted records Waiting on every server left Calibrating, since
// this server runs every calibration job and none runs at startup; the operator
// resumes them when the server is idle. Call it before serving.
func (s *Set) RecoverInterrupted(ctx context.Context) error {
	servers, err := s.Cluster.Servers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range servers {
		if p.Status.Calibration == nil || p.Status.Calibration.Phase != PhaseCalibrating {
			continue
		}
		if _, err := s.record(ctx, p.Name, PhaseWaiting, "", interruptedMessage, nil); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// requestFor is a probe of model on the server, with the server's limits.
func requestFor(p *aiv1alpha1.ModelServer, model, kind string) probe.Request {
	size, _ := sizeOf(p, model)
	engineType := ""
	if cluster.IsVLLM(p) {
		engineType = string(p.Spec.Type)
	}
	return probe.Request{
		Server: p.Name, Endpoint: p.EndpointFor(model), Model: model, Kind: kind, EngineType: engineType,
		MaxParallel: cluster.MaxParallel(p), SizeMiB: size,
		MemoryMiB: cluster.UsableMemoryMiB(p), Accelerator: cluster.HasAccelerator(p),
	}
}

// probeOnce sends one probe; a busy server is errBusy.
func (s *Set) probeOnce(ctx context.Context, p *aiv1alpha1.ModelServer, model, kind string) (probe.Result, error) {
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
// server since its calibration, alone and alongside other calls, beside the
// calibrated speed, and which models drifted by the operator's rule.
func (s *Set) observedPerformance(ctx context.Context, raw json.RawMessage) (any, error) {
	var args serverArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	if err := required(fieldServer, args.Server); err != nil {
		return nil, err
	}
	p, err := s.Cluster.Server(ctx, args.Server)
	if err != nil {
		return nil, err
	}
	entries, err := s.History.Of(p.Name)
	if err != nil {
		return nil, fmt.Errorf("reading the performance history: %w", err)
	}
	others, err := s.Tickets.Others(p.Name)
	if err != nil {
		return nil, fmt.Errorf("reading the server's tickets: %w", err)
	}
	calibrated, since := perf.CalibratedSpeeds(p.Status.Calibration)
	observed := perf.Observe(entries, p.Name, since)
	return map[string]any{
		fieldServer:   p.Name,
		"calibration": p.Status.Calibration,
		"callsNow":    others,
		"observed":    observed,
		"drifted":     perf.Drifted(calibrated, observed),
		"driftRule": fmt.Sprintf("median speed of warm crew calls running alone below %.0f%% or above %.0f%% "+
			"of the calibrated speed, over at least %d calls since the calibration",
			perf.MinRatio*100, perf.MaxRatio*100, perf.MinSamples),
	}, nil
}
