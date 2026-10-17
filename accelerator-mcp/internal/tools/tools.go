/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tools is the accelerator MCP tool set: read what the cluster's model
// providers are and hold, probe a model on one, and record a calibration.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/cluster"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/store"
)

// Tool names, as MCP clients call them.
const (
	ToolListProviders     = "list_providers"
	ToolProviderState     = "provider_state"
	ToolProbeModel        = "probe_model"
	ToolRecordCalibration = "record_calibration"
)

// Argument and schema field names used in more than one place.
const (
	fieldProvider    = "provider"
	fieldModel       = "model"
	fieldPhase       = "phase"
	fieldProbe       = "probe"
	fieldType        = "type"
	fieldDescription = "description"
	fieldModels      = "models"
	typeString       = "string"
)

// resetScopeAll is the reset scope that also forgets the provider's performance history.
const resetScopeAll = "all"

// Calibration phases record_calibration accepts.
const (
	PhaseCalibrating = "Calibrating"
	PhaseCalibrated  = cluster.PhaseCalibrated
	PhaseWaiting     = "Waiting"
	PhaseFailed      = "Failed"
)

// Cluster is what the tools read and write in Kubernetes.
type Cluster interface {
	Providers(ctx context.Context) ([]aiv1alpha1.ModelProvider, error)
	Provider(ctx context.Context, name string) (*aiv1alpha1.ModelProvider, error)
	BoundModels(ctx context.Context, provider string) ([]string, error)
	RecordCalibration(ctx context.Context, p *aiv1alpha1.ModelProvider,
		update func(*aiv1alpha1.ProviderCalibration, *aiv1alpha1.ModelProvider)) error
}

// Prober runs probes and unloads what they loaded.
type Prober interface {
	Probe(ctx context.Context, req probe.Request) (probe.Result, error)
	Finish(ctx context.Context, provider string) ([]string, error)
}

// Set is the tool set.
type Set struct {
	Cluster Cluster
	Prober  Prober
	Tickets store.Tickets
	History store.History
	Engine  func(endpoint string) engine.Engine
	Now     func() time.Time
	// Jobs runs calibrate_provider recipes in the background.
	Jobs *Jobs
}

// Call runs the named tool with its JSON arguments and returns its JSON result.
func (s *Set) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var out any
	var err error
	switch name {
	case ToolListProviders:
		out, err = s.listProviders(ctx)
	case ToolProviderState:
		out, err = s.providerState(ctx, args)
	case ToolProbeModel:
		out, err = s.probeModel(ctx, args)
	case ToolRecordCalibration:
		out, err = s.recordCalibration(ctx, args)
	case ToolCalibrateProvider:
		out, err = s.calibrateProvider(ctx, args)
	case ToolObservedPerformance:
		out, err = s.observedPerformance(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

// providerSummary is one provider in list_providers.
type providerSummary struct {
	Name          string                          `json:"name"`
	Namespace     string                          `json:"namespace"`
	Ready         bool                            `json:"ready"`
	Engine        string                          `json:"engine"`
	EngineVersion string                          `json:"engineVersion,omitempty"`
	MemoryMiB     int64                           `json:"memoryMiB"`
	UsableMiB     int64                           `json:"usableMiB"`
	Accelerator   bool                            `json:"accelerator"`
	Calibrate     bool                            `json:"calibrationEnabled"`
	MaxParallel   int                             `json:"maxParallel"`
	ContextLength int                             `json:"contextLength,omitempty"`
	Calibration   *aiv1alpha1.ProviderCalibration `json:"calibration,omitempty"`
	Drift         *aiv1alpha1.CalibrationDrift    `json:"calibrationDrift,omitempty"`
	ResetRequest  *resetRequest                   `json:"resetRequested,omitempty"`
}

type resetRequest struct {
	Token string `json:"token"`
	Scope string `json:"scope"`
}

func (s *Set) listProviders(ctx context.Context) (any, error) {
	providers, err := s.Cluster.Providers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]providerSummary, 0, len(providers))
	for i := range providers {
		out = append(out, summarize(&providers[i]))
	}
	return map[string]any{"providers": out}, nil
}

func summarize(p *aiv1alpha1.ModelProvider) providerSummary {
	sum := providerSummary{
		Name: p.Name, Namespace: p.Namespace, Ready: p.Status.Ready, Engine: string(p.Spec.Type),
		MemoryMiB: cluster.TotalMemoryMiB(p), UsableMiB: cluster.UsableMemoryMiB(p),
		Accelerator: cluster.HasAccelerator(p), MaxParallel: cluster.MaxParallel(p),
		Calibrate:   cluster.CalibrationEnabled(p),
		Calibration: p.Status.Calibration,
		Drift:       p.Status.CalibrationDrift,
	}
	if p.Status.ProviderInfo != nil {
		sum.EngineVersion = p.Status.ProviderInfo.Version
	}
	if p.Status.Capacity != nil {
		sum.ContextLength = p.Status.Capacity.ContextLength
	}
	if cluster.ResetRequested(p) {
		sum.ResetRequest = &resetRequest{Token: p.Annotations[cluster.AnnotationReset],
			Scope: scopeOf(p)}
	}
	return sum
}

func scopeOf(p *aiv1alpha1.ModelProvider) string {
	if p.Annotations[cluster.AnnotationResetScope] == resetScopeAll {
		return resetScopeAll
	}
	return "calibration"
}

type providerArgs struct {
	Provider string `json:"provider"`
}

// parse decodes the arguments strictly: an unknown field is an error, as the
// tool's schema (additionalProperties false) says.
func parse(args json.RawMessage, into any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func required(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func (s *Set) providerState(ctx context.Context, raw json.RawMessage) (any, error) {
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
	loaded, err := s.Engine(p.Spec.Endpoint).Loaded(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading loaded models: %w", err)
	}
	others, _ := s.Tickets.Others(p.Name)
	inUse, _ := s.Tickets.ModelsInUse(p.Name)
	bound, _ := s.Cluster.BoundModels(ctx, p.Name)
	return map[string]any{
		fieldProvider: summarize(p),
		"loaded":      loadedModels(loaded),
		"otherCalls":  others,
		"modelsInUse": keysOf(inUse),
		"available":   available(p),
		"boundModels": bound,
	}, nil
}

type loadedModel struct {
	Model          string `json:"model"`
	SizeMiB        int64  `json:"sizeMiB"`
	AcceleratorMiB int64  `json:"acceleratorMiB"`
	ContextLength  int    `json:"contextLength"`
}

func loadedModels(residents []engine.Resident) []loadedModel {
	out := make([]loadedModel, 0, len(residents))
	for _, r := range residents {
		out = append(out, loadedModel{Model: r.Model, SizeMiB: r.SizeMiB, AcceleratorMiB: r.VRAMMiB,
			ContextLength: r.ContextLength})
	}
	return out
}

type availableModel struct {
	Model   string `json:"model"`
	SizeMiB int64  `json:"sizeMiB"`
}

func available(p *aiv1alpha1.ModelProvider) []availableModel {
	out := []availableModel{}
	if p.Status.Capacity == nil {
		return out
	}
	for _, m := range p.Status.Capacity.AvailableModels {
		out = append(out, availableModel{Model: m.Name, SizeMiB: m.SizeBytes / (1024 * 1024)})
	}
	return out
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type probeArgs struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Probe    string `json:"probe"`
}

func (s *Set) probeModel(ctx context.Context, raw json.RawMessage) (any, error) {
	var args probeArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	needed := map[string]string{fieldProvider: args.Provider, fieldModel: args.Model, fieldProbe: args.Probe}
	for name, value := range needed {
		if err := required(name, value); err != nil {
			return nil, err
		}
	}
	p, err := s.Cluster.Provider(ctx, args.Provider)
	if err != nil {
		return nil, err
	}
	if !p.Status.Ready {
		return nil, fmt.Errorf("provider %s is not ready", p.Name)
	}
	if !cluster.CalibrationEnabled(p) {
		return nil, fmt.Errorf("provider %s has spec.calibration.enabled false", p.Name)
	}
	if _, ok := sizeOf(p, args.Model); !ok {
		return nil, fmt.Errorf("%s has not reported %s among its downloaded models; provider_state lists them",
			p.Name, args.Model)
	}
	if err := s.notDuringAJob(p.Name); err != nil {
		return nil, err
	}
	return s.Prober.Probe(ctx, requestFor(p, args.Model, args.Probe))
}

// notDuringAJob refuses a tool that would disturb a calibrate_provider run: a
// probe or a record unloading its model would make the next measurement cold.
func (s *Set) notDuringAJob(provider string) error {
	if s.Jobs.Running(provider) {
		return fmt.Errorf("a calibrate_provider run is in progress on %s; list_providers shows when it ends", provider)
	}
	return nil
}

// sizeOf is the model's size on disk as the provider reports it; false when the
// provider has not reported the model, so the memory check cannot be made.
func sizeOf(p *aiv1alpha1.ModelProvider, model string) (int64, bool) {
	for _, m := range available(p) {
		if m.Model == model {
			return m.SizeMiB, true
		}
	}
	return 0, false
}

type recordArgs struct {
	Provider string        `json:"provider"`
	Phase    string        `json:"phase"`
	Message  string        `json:"message"`
	Models   []modelResult `json:"models"`
}

// modelResult is one model's result as the agent reports it; fits is required.
type modelResult struct {
	Model                           string `json:"model"`
	Fits                            *bool  `json:"fits"`
	Reason                          string `json:"reason"`
	LoadMs                          int64  `json:"loadMs"`
	ResidentMiB                     int64  `json:"residentMiB"`
	ContextLength                   int    `json:"contextLength"`
	PromptTokensPerSecond           int64  `json:"promptTokensPerSecond"`
	GenerateTokensPerSecond         int64  `json:"generateTokensPerSecond"`
	ParallelRequests                int    `json:"parallelRequests"`
	ParallelGenerateTokensPerSecond int64  `json:"parallelGenerateTokensPerSecond"`
}

// calibration checks the result and converts it to the status type.
func (m modelResult) calibration() (aiv1alpha1.ModelCalibration, error) {
	if m.Model == "" || m.Fits == nil {
		return aiv1alpha1.ModelCalibration{}, fmt.Errorf("every model result needs model and fits")
	}
	if min(m.LoadMs, m.ResidentMiB, m.PromptTokensPerSecond, m.GenerateTokensPerSecond,
		m.ParallelGenerateTokensPerSecond, int64(m.ContextLength), int64(m.ParallelRequests)) < 0 {
		return aiv1alpha1.ModelCalibration{}, fmt.Errorf("%s: measurements cannot be negative", m.Model)
	}
	return aiv1alpha1.ModelCalibration{
		Model: m.Model, Fits: *m.Fits, Reason: m.Reason, LoadMs: m.LoadMs, ResidentMiB: m.ResidentMiB,
		ContextLength: m.ContextLength, PromptTokensPerSecond: m.PromptTokensPerSecond,
		GenerateTokensPerSecond: m.GenerateTokensPerSecond, ParallelRequests: m.ParallelRequests,
		ParallelGenerateTokensPerSecond: m.ParallelGenerateTokensPerSecond,
	}, nil
}

func calibrations(results []modelResult) ([]aiv1alpha1.ModelCalibration, error) {
	out := make([]aiv1alpha1.ModelCalibration, 0, len(results))
	for _, r := range results {
		c, err := r.calibration()
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func validPhase(phase string) bool {
	switch phase {
	case PhaseCalibrating, PhaseCalibrated, PhaseWaiting, PhaseFailed:
		return true
	}
	return false
}

func (s *Set) recordCalibration(ctx context.Context, raw json.RawMessage) (any, error) {
	var args recordArgs
	if err := parse(raw, &args); err != nil {
		return nil, err
	}
	if err := required(fieldProvider, args.Provider); err != nil {
		return nil, err
	}
	if !validPhase(args.Phase) {
		return nil, fmt.Errorf("phase must be Calibrating, Calibrated, Waiting, or Failed")
	}
	models, err := calibrations(args.Models)
	if err != nil {
		return nil, err
	}
	if err := s.notDuringAJob(args.Provider); err != nil {
		return nil, err
	}
	return s.record(ctx, args.Provider, args.Phase, args.Message, models)
}

// record writes a calibration phase and its results on the provider. Starting
// (Calibrating) acts on a pending reset; any other phase ends the calibration and
// unloads the models its probes loaded.
func (s *Set) record(ctx context.Context, provider, phase, message string,
	models []aiv1alpha1.ModelCalibration) (map[string]any, error) {
	p, err := s.Cluster.Provider(ctx, provider)
	if err != nil {
		return nil, err
	}
	forgot, err := s.forgetOnReset(p, phase)
	if err != nil {
		return nil, fmt.Errorf("forgetting the provider's history for the reset: %w", err)
	}
	if err := s.Cluster.RecordCalibration(ctx, p, s.apply(phase, message, models)); err != nil {
		return nil, err
	}
	out := map[string]any{fieldProvider: p.Name, fieldPhase: phase, "historyForgotten": forgot}
	if phase != PhaseCalibrating {
		unloaded, err := s.Prober.Finish(ctx, p.Name)
		out["unloaded"] = unloaded
		if err != nil {
			out["unloadError"] = err.Error()
		}
	}
	return out, nil
}

// forgetOnReset forgets the provider's performance history when a calibration
// starts on a reset whose scope is all. A failure is returned before anything is
// recorded, so the reset stays pending.
func (s *Set) forgetOnReset(p *aiv1alpha1.ModelProvider, phase string) (bool, error) {
	if phase != PhaseCalibrating || !cluster.ResetRequested(p) || scopeOf(p) != resetScopeAll {
		return false, nil
	}
	if err := s.History.Forget(p.Name); err != nil {
		return false, err
	}
	return true, nil
}

// apply returns the status update for a record_calibration call. A pending reset
// is acted on once, when a calibration starts: earlier results are cleared and its
// token recorded, so resuming after a pause does not clear the new results again,
// and a reset requested during the run stays pending for the next calibration.
func (s *Set) apply(phase, message string,
	models []aiv1alpha1.ModelCalibration) func(*aiv1alpha1.ProviderCalibration, *aiv1alpha1.ModelProvider) {
	return func(cal *aiv1alpha1.ProviderCalibration, p *aiv1alpha1.ModelProvider) {
		now := s.now()
		cal.Phase = phase
		cal.Message = message
		cal.UpdatedAt = &now
		if phase == PhaseCalibrating && cluster.ResetRequested(p) {
			cal.Models = nil
			cal.ResetToken = p.Annotations[cluster.AnnotationReset]
		}
		cal.Models = merge(cal.Models, models, now)
		if phase == PhaseCalibrated {
			cal.CompletedAt = &now
			if p.Status.ProviderInfo != nil {
				cal.EngineVersion = p.Status.ProviderInfo.Version
			}
		}
	}
}

func (s *Set) now() metav1.Time {
	return metav1.NewTime(s.Now())
}

// merge keeps earlier results and replaces those measured again, stamping new ones.
func merge(earlier, latest []aiv1alpha1.ModelCalibration, now metav1.Time) []aiv1alpha1.ModelCalibration {
	out := append([]aiv1alpha1.ModelCalibration(nil), earlier...)
	index := map[string]int{}
	for i, m := range out {
		index[m.Model] = i
	}
	for _, m := range latest {
		stamped := m
		stamped.CalibratedAt = &now
		if i, ok := index[m.Model]; ok {
			out[i] = stamped
			continue
		}
		index[m.Model] = len(out)
		out = append(out, stamped)
	}
	return out
}
