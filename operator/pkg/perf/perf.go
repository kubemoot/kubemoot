/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package perf reads the scheduler's performance history against a server's
// calibration: how fast each model generated for crew calls since it was
// calibrated, alone on the server and alongside other calls, and whether that
// has drifted from what calibration measured. The operator uses it to notice
// drift; the accelerator-mcp tools use it to show the evidence.
package perf

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// KV is the part of a NATS KV store that ReadHistory reads.
type KV interface {
	ListKVKeys(bucket string) ([]string, error)
	GetKVValue(bucket, key string) ([]byte, error)
}

// ReadHistory reads every agent's performance history for the server. An entry
// that cannot be read is an error; one that cannot be parsed is skipped.
func ReadHistory(kv KV, server string) ([]bus.SchedulerPerformance, error) {
	keys, err := kv.ListKVKeys(bus.PerformanceBucket)
	if err != nil {
		return nil, err
	}
	prefix := bus.PerformanceKeyPrefix(server)
	var out []bus.SchedulerPerformance
	for _, key := range keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		raw, err := kv.GetKVValue(bus.PerformanceBucket, key)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", key, err)
		}
		var entry bus.SchedulerPerformance
		if raw != nil && json.Unmarshal(raw, &entry) == nil {
			out = append(out, entry)
		}
	}
	return out, nil
}

// CalibratedSpeeds is each fitting model's calibrated generation speed, and when
// the calibration completed (epoch milliseconds; 0 when it has not).
func CalibratedSpeeds(cal *aiv1alpha1.ServerCalibration) (map[string]int64, int64) {
	speeds := map[string]int64{}
	if cal == nil {
		return speeds, 0
	}
	for _, m := range cal.Models {
		if m.Fits && m.HasSpeeds() {
			speeds[m.Model] = m.GenerateTokensPerSecond
		}
	}
	var since int64
	if cal.CompletedAt != nil {
		since = cal.CompletedAt.UnixMilli()
	}
	return speeds, since
}

// Drift thresholds. A model has drifted when crew calls running alone on the
// server generated, at the median, below MinRatio or above MaxRatio of the
// calibrated speed, over at least MinSamples such calls since the calibration. A
// call runs alone when it was the only call holding the server as it started;
// others may still join it while it generates.
const (
	MinSamples = 8
	MinRatio   = 0.6
	MaxRatio   = 1.0 / MinRatio
)

// Observation is what crew calls showed for one model on one server.
type Observation struct {
	Model string `json:"model"`
	// SoloTokensPerSecond is the median generation speed of successful warm calls
	// that ran alone on the server; 0 with no such calls.
	SoloTokensPerSecond int64 `json:"soloTokensPerSecond"`
	SoloSamples         int   `json:"soloSamples"`
	// SoloEarlierTokensPerSecond and SoloLaterTokensPerSecond are the medians of the
	// earlier and later half of those calls, in time order, so a change that lasted
	// can be told from one that passed; 0 with fewer than two calls.
	SoloEarlierTokensPerSecond int64 `json:"soloEarlierTokensPerSecond"`
	SoloLaterTokensPerSecond   int64 `json:"soloLaterTokensPerSecond"`
	// SharedTokensPerSecond is the same for calls that ran alongside others.
	SharedTokensPerSecond int64 `json:"sharedTokensPerSecond"`
	SharedSamples         int   `json:"sharedSamples"`
	// MaxActiveCalls is the most calls seen on the server at once.
	MaxActiveCalls int64 `json:"maxActiveCalls"`
	// Failures counts failed or timed-out calls among the samples.
	Failures int `json:"failures"`
	// FirstAt and LastAt bound the samples, epoch milliseconds.
	FirstAt int64 `json:"firstAt"`
	LastAt  int64 `json:"lastAt"`
}

// Observe summarizes the crew calls on server that started at or after since
// (epoch milliseconds), per model, sorted by model. Calibration probes, cold
// starts, and calls with no engine timing are left out of the speeds.
func Observe(entries []bus.SchedulerPerformance, server string, since int64) []Observation {
	byModel := map[string]*collector{}
	for _, e := range entries {
		if e.Server != server || e.Model == "" {
			continue
		}
		c := byModel[e.Model]
		if c == nil {
			c = &collector{obs: Observation{Model: e.Model}}
			byModel[e.Model] = c
		}
		for _, s := range e.Samples {
			if s.At >= since && s.Kind != bus.SchedulerPerformanceSamplesElemKindCalibration {
				c.add(s)
			}
		}
	}
	out := make([]Observation, 0, len(byModel))
	for _, c := range byModel {
		if c.seen > 0 {
			out = append(out, c.result())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

type collector struct {
	obs    Observation
	seen   int
	solo   []timed
	shared []int64
}

// timed is a speed and when its call started.
type timed struct {
	at, tps int64
}

func (c *collector) add(s bus.SchedulerPerformanceSamplesElem) {
	c.seen++
	if c.obs.FirstAt == 0 || s.At < c.obs.FirstAt {
		c.obs.FirstAt = s.At
	}
	c.obs.LastAt = max(c.obs.LastAt, s.At)
	c.obs.MaxActiveCalls = max(c.obs.MaxActiveCalls, s.ActiveCalls)
	if !s.Ok {
		c.obs.Failures++
		return
	}
	tps, ok := generateSpeed(s)
	if !ok || s.ColdStart {
		return
	}
	switch {
	case s.ActiveCalls == 1:
		c.solo = append(c.solo, timed{at: s.At, tps: tps})
	case s.ActiveCalls > 1:
		c.shared = append(c.shared, tps)
	}
}

func (c *collector) result() Observation {
	o := c.obs
	slices.SortStableFunc(c.solo, func(a, b timed) int { return cmp.Compare(a.at, b.at) })
	solo := make([]int64, 0, len(c.solo))
	for _, t := range c.solo {
		solo = append(solo, t.tps)
	}
	o.SoloTokensPerSecond, o.SoloSamples = median(solo), len(solo)
	if len(solo) >= 2 {
		half := len(solo) / 2
		o.SoloEarlierTokensPerSecond, o.SoloLaterTokensPerSecond = median(solo[:half]), median(solo[half:])
	}
	o.SharedTokensPerSecond, o.SharedSamples = median(c.shared), len(c.shared)
	return o
}

// generateSpeed is the sample's generation speed in tokens per second, from the
// engine's own timing; false when the engine reported none.
func generateSpeed(s bus.SchedulerPerformanceSamplesElem) (int64, bool) {
	if s.EvalMs <= 0 || s.OutputTokens <= 0 {
		return 0, false
	}
	return s.OutputTokens * 1000 / s.EvalMs, true
}

func median[T int64 | float64](values []T) T {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// ModelDrift is a model whose observed speed has drifted from its calibration.
type ModelDrift struct {
	Model                     string `json:"model"`
	CalibratedTokensPerSecond int64  `json:"calibratedTokensPerSecond"`
	ObservedTokensPerSecond   int64  `json:"observedTokensPerSecond"`
	Samples                   int    `json:"samples"`
}

// Drifted compares the observations with the calibrated generation speeds (by
// model) and returns the models that drifted, in the observations' order; an
// empty list, never nil, when none did.
func Drifted(calibrated map[string]int64, observed []Observation) []ModelDrift {
	out := []ModelDrift{}
	for _, o := range observed {
		want := calibrated[o.Model]
		if want <= 0 || o.SoloSamples < MinSamples || o.SoloTokensPerSecond <= 0 {
			continue
		}
		ratio := float64(o.SoloTokensPerSecond) / float64(want)
		if ratio < MinRatio || ratio > MaxRatio {
			out = append(out, ModelDrift{Model: o.Model, CalibratedTokensPerSecond: want,
				ObservedTokensPerSecond: o.SoloTokensPerSecond, Samples: o.SoloSamples})
		}
	}
	return out
}

// Speeds are a model's calibrated speeds, in tokens per second.
type Speeds struct {
	PromptTokensPerSecond   int64
	GenerateTokensPerSecond int64
}

// LoadFactor is how much longer calls took, at the median, once at least ActiveCalls
// calls held the server as they started (themselves included), than calls that ran alone,
// over Samples calls.
type LoadFactor struct {
	ActiveCalls int64   `json:"activeCalls"`
	Factor      float64 `json:"factor"`
	Samples     int     `json:"samples"`
}

// MinLoadSamples is the fewest calls a load level needs, and the solo calls it is compared
// with, for its factor to be published.
const MinLoadSamples = 5

// loadLevels are the lowest numbers of calls on the server at which each level starts.
var loadLevels = []int64{1, 2, 3, 5, 9}

// Contention is, per model, how much slower crew calls ran as more calls shared the
// server. Each call's time is divided by the time its tokens would take at the model's
// calibrated speeds, so calls of different lengths compare; a level's factor is its median
// over the median of the calls that ran alone. A level without enough calls, or a model
// without enough solo calls or calibrated speeds, publishes nothing, and a factor never
// falls below the one of a lower level. Calibration probes, cold starts, failed calls and
// calls that started before since (epoch milliseconds) are left out.
func Contention(entries []bus.SchedulerPerformance, server string, since int64,
	speeds map[string]Speeds) map[string][]LoadFactor {
	out := map[string][]LoadFactor{}
	for model, byLevel := range loadRatios(entries, server, since, speeds) {
		if factors := loadFactors(byLevel); len(factors) > 0 {
			out[model] = factors
		}
	}
	return out
}

// loadRatios are the load ratios of the crew calls on server, per model and load level.
func loadRatios(entries []bus.SchedulerPerformance, server string, since int64,
	speeds map[string]Speeds) map[string][][]float64 {
	ratios := map[string][][]float64{}
	for _, e := range entries {
		sp, ok := speeds[e.Model]
		if e.Server != server || !ok || sp.PromptTokensPerSecond <= 0 || sp.GenerateTokensPerSecond <= 0 {
			continue
		}
		for _, s := range e.Samples {
			r, level, ok := loadRatio(s, sp, since)
			if !ok {
				continue
			}
			if ratios[e.Model] == nil {
				ratios[e.Model] = make([][]float64, len(loadLevels))
			}
			ratios[e.Model][level] = append(ratios[e.Model][level], r)
		}
	}
	return ratios
}

// loadRatio is a crew call's time over the time its tokens take at the calibrated speeds,
// and the load level it ran at; false for a call that tells nothing about load.
func loadRatio(s bus.SchedulerPerformanceSamplesElem, sp Speeds, since int64) (float64, int, bool) {
	if !s.Ok || s.ColdStart || s.At < since || s.DurationMs <= 0 || s.ActiveCalls < 1 ||
		s.Kind == bus.SchedulerPerformanceSamplesElemKindCalibration {
		return 0, 0, false
	}
	modeled := float64(s.PromptTokens)/float64(sp.PromptTokensPerSecond) +
		float64(s.OutputTokens)/float64(sp.GenerateTokensPerSecond)
	if modeled <= 0 {
		return 0, 0, false
	}
	level := 0
	for i, from := range loadLevels {
		if s.ActiveCalls >= from {
			level = i
		}
	}
	return float64(s.DurationMs) / 1000 / modeled, level, true
}

// loadFactors are the published factors of the levels above the solo one.
func loadFactors(byLevel [][]float64) []LoadFactor {
	if len(byLevel[0]) < MinLoadSamples {
		return nil
	}
	solo := median(byLevel[0])
	var out []LoadFactor
	floor := 1.0
	for i := 1; i < len(loadLevels); i++ {
		if len(byLevel[i]) < MinLoadSamples {
			continue
		}
		floor = max(floor, math.Round(median(byLevel[i])/solo*100)/100)
		if floor > 1.0 {
			out = append(out, LoadFactor{ActiveCalls: loadLevels[i], Factor: floor, Samples: len(byLevel[i])})
		}
	}
	return out
}
