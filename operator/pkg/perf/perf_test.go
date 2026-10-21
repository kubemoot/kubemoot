/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package perf

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

const (
	gpu   = "gpu-a"
	small = "small:8b"
	big   = "big:32b"
)

// sample is a warm, successful crew call generating at tps with active calls on the server.
func sample(at, tps, active int64) bus.SchedulerPerformanceSamplesElem {
	return bus.SchedulerPerformanceSamplesElem{At: at, Ok: true, ActiveCalls: active,
		Kind: bus.SchedulerPerformanceSamplesElemKindToolTurn, OutputTokens: tps, EvalMs: 1000}
}

func entry(server, model string, samples ...bus.SchedulerPerformanceSamplesElem) bus.SchedulerPerformance {
	return bus.SchedulerPerformance{Server: server, Model: model, Agent: "crew/a", Samples: samples}
}

func TestObserve_splitsSoloFromSharedCalls_andLeavesOutWhatIsNotComparable(t *testing.T) {
	cold := sample(5, 10, 1)
	cold.ColdStart = true
	failed := sample(6, 10, 1)
	failed.Ok = false
	probe := sample(7, 999, 1)
	probe.Kind = bus.SchedulerPerformanceSamplesElemKindCalibration
	untimed := sample(8, 10, 1)
	untimed.EvalMs = -1
	before := sample(1, 999, 1)

	obs := Observe([]bus.SchedulerPerformance{
		entry(gpu, small, sample(10, 100, 1), sample(11, 120, 1), sample(12, 110, 1), sample(12, 999, 0), before, cold,
			failed, probe, untimed),
		entry(gpu, small, sample(13, 60, 3), sample(14, 40, 2)), // another agent's entry for the same model
		entry("other", small, sample(10, 5, 1)),
		entry(gpu, "", sample(10, 5, 1)),
	}, gpu, 5)

	if len(obs) != 1 || obs[0].Model != small {
		t.Fatalf("observations: %+v", obs)
	}
	// The sample with no active-call count is neither alone nor shared.
	want := Observation{Model: small, SoloTokensPerSecond: 110, SoloSamples: 3, SoloEarlierTokensPerSecond: 100,
		SoloLaterTokensPerSecond: 115, SharedTokensPerSecond: 50, SharedSamples: 2, MaxActiveCalls: 3, Failures: 1,
		FirstAt: 5, LastAt: 14}
	if obs[0] != want {
		t.Errorf("got  %+v\nwant %+v", obs[0], want)
	}
}

func TestObserve_noSamplesSinceTheCalibration(t *testing.T) {
	if obs := Observe([]bus.SchedulerPerformance{entry(gpu, small, sample(1, 100, 1))}, gpu, 10); len(obs) != 0 {
		t.Errorf("observations from before the calibration: %+v", obs)
	}
	if obs := Observe(nil, gpu, 0); len(obs) != 0 {
		t.Errorf("observations from nothing: %+v", obs)
	}
}

func solo(model string, n int, tps int64) Observation {
	return Observation{Model: model, SoloSamples: n, SoloTokensPerSecond: tps}
}

func TestDrifted(t *testing.T) {
	calibrated := map[string]int64{small: 100, big: 50}
	cases := []struct {
		name string
		obs  []Observation
		want []string
	}{
		{"within the band", []Observation{solo(small, MinSamples, 70), solo(big, MinSamples, 80)}, nil},
		{"slower", []Observation{solo(small, MinSamples, 59)}, []string{small}},
		{"faster", []Observation{solo(big, MinSamples, 84)}, []string{big}},
		{"too few samples", []Observation{solo(small, MinSamples-1, 10)}, nil},
		{"exactly the minimum ratio", []Observation{solo(small, MinSamples, 60)}, nil},
		{"just below the minimum ratio", []Observation{solo(small, MinSamples, 59)}, []string{small}},
		{"at the maximum ratio", []Observation{solo(big, MinSamples, 83)}, nil},
		{"never calibrated", []Observation{solo("new:1b", MinSamples, 10)}, nil},
		{"no solo speed", []Observation{solo(small, MinSamples, 0)}, nil},
		{"sorted as observed", []Observation{solo(big, MinSamples, 10), solo(small, MinSamples, 10)}, []string{big, small}},
	}
	if got := Drifted(calibrated, nil); got == nil || len(got) != 0 {
		t.Errorf("no observations: %#v, want an empty list", got)
	}
	for _, c := range cases {
		got := Drifted(calibrated, c.obs)
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v", c.name, got)
			continue
		}
		for i, d := range got {
			if d.Model != c.want[i] || d.CalibratedTokensPerSecond != calibrated[d.Model] || d.Samples != MinSamples {
				t.Errorf("%s: %+v", c.name, d)
			}
		}
	}
}

func TestMedian(t *testing.T) {
	cases := map[int64][]int64{0: nil, 5: {5}, 6: {9, 3}, 4: {4, 1, 9}}
	for want, values := range cases {
		if got := median(values); got != want {
			t.Errorf("median(%v) = %d, want %d", values, got, want)
		}
	}
}

// Earlier and later medians follow the calls' start times, not the order recorded.
func TestObserve_earlierAndLaterHalvesInTimeOrder(t *testing.T) {
	obs := Observe([]bus.SchedulerPerformance{
		entry(gpu, small, sample(40, 50, 1), sample(30, 50, 1)),
		entry(gpu, small, sample(20, 150, 1), sample(10, 150, 1)),
	}, gpu, 0)
	if len(obs) != 1 || obs[0].SoloEarlierTokensPerSecond != 150 || obs[0].SoloLaterTokensPerSecond != 50 {
		t.Errorf("halves: %+v", obs)
	}
	one := Observe([]bus.SchedulerPerformance{entry(gpu, small, sample(10, 80, 1))}, gpu, 0)
	if one[0].SoloEarlierTokensPerSecond != 0 || one[0].SoloLaterTokensPerSecond != 0 {
		t.Errorf("one call has no halves: %+v", one)
	}
}

func TestCalibratedSpeeds(t *testing.T) {
	speeds, since := CalibratedSpeeds(nil)
	if len(speeds) != 0 || since != 0 {
		t.Errorf("no calibration: %v %d", speeds, since)
	}
	done := metav1.NewTime(time.Unix(1700000000, 0))
	speeds, since = CalibratedSpeeds(&aiv1alpha1.ServerCalibration{CompletedAt: &done,
		Models: []aiv1alpha1.ModelCalibration{{Model: small, Fits: true, GenerateTokensPerSecond: 100},
			{Model: big, Fits: false, GenerateTokensPerSecond: 5}}})
	if speeds[small] != 100 || len(speeds) != 1 || since != done.UnixMilli() {
		t.Errorf("speeds %v since %d", speeds, since)
	}
}

// memKV is a map keyed by bucket/key.
type memKV map[string][]byte

func (m memKV) ListKVKeys(bucket string) ([]string, error) {
	var keys []string
	for k := range m {
		if b, key, ok := strings.Cut(k, "/"); ok && b == bucket {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (m memKV) GetKVValue(bucket, key string) ([]byte, error) {
	if bucket == "fail" {
		return nil, errors.New("down")
	}
	return m[bucket+"/"+key], nil
}

func TestReadHistory(t *testing.T) {
	kv := memKV{}
	raw, _ := json.Marshal(entry(gpu, small, sample(1, 10, 1)))
	kv[bus.PerformanceBucket+"/"+bus.PerformanceKey(gpu, small, "crew/a")] = raw
	kv[bus.PerformanceBucket+"/"+bus.PerformanceKey(gpu, big, "crew/a")] = []byte(`{"samples":`)
	kv[bus.PerformanceBucket+"/"+bus.PerformanceKey(gpu+"x", small, "crew/a")] = raw
	got, err := ReadHistory(kv, gpu)
	if err != nil || len(got) != 1 || got[0].Model != small {
		t.Errorf("history: %+v %v", got, err)
	}
}

// call is a warm, successful crew call of 1000 prompt and 100 output tokens that took ms, with
// active calls on the server as it started. At 1000 and 100 tokens per second its modeled time is
// 2 seconds.
func call(at, ms, active int64) bus.SchedulerPerformanceSamplesElem {
	return bus.SchedulerPerformanceSamplesElem{At: at, Ok: true, ActiveCalls: active, DurationMs: ms,
		PromptTokens: 1000, OutputTokens: 100, Kind: bus.SchedulerPerformanceSamplesElemKindToolTurn}
}

func repeat(n int, s bus.SchedulerPerformanceSamplesElem) []bus.SchedulerPerformanceSamplesElem {
	out := make([]bus.SchedulerPerformanceSamplesElem, n)
	for i := range out {
		out[i] = s
		out[i].At += int64(i)
	}
	return out
}

var calibrated = map[string]Speeds{small: {PromptTokensPerSecond: 1000, GenerateTokensPerSecond: 100}}

func TestContention_isHowMuchLongerCallsTookAsMoreSharedTheServer(t *testing.T) {
	samples := slices.Concat(
		repeat(6, call(100, 2000, 1)),
		repeat(6, call(200, 2200, 2)),
		repeat(6, call(300, 3000, 6)),
		repeat(6, call(400, 4000, 12)),
	)

	got := Contention([]bus.SchedulerPerformance{entry(gpu, small, samples...)}, gpu, 0, calibrated)[small]

	want := []LoadFactor{{2, 1.1, 6}, {5, 1.5, 6}, {9, 2.0, 6}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("level %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestContention_isComparedWithSoloCallsOfTheSameModelNotWithTheCalibrationAlone(t *testing.T) {
	// Every call takes twice as long as modeled (hidden reasoning, say), alone or not: no load effect.
	samples := slices.Concat(
		repeat(6, call(100, 4000, 1)),
		repeat(6, call(200, 4000, 6)),
	)

	if got := Contention([]bus.SchedulerPerformance{entry(gpu, small, samples...)}, gpu, 0, calibrated); len(got) != 0 {
		t.Errorf("a constant offset is not load: %+v", got)
	}
}

func TestContention_publishesNothingItCannotTrust(t *testing.T) {
	cold := call(100, 9000, 1)
	cold.ColdStart = true
	failed := call(100, 9000, 1)
	failed.Ok = false
	probe := call(100, 9000, 1)
	probe.Kind = bus.SchedulerPerformanceSamplesElemKindCalibration
	empty := call(100, 9000, 1)
	empty.PromptTokens, empty.OutputTokens = 0, 0
	noDuration := call(100, 0, 1)
	before := call(1, 9000, 1)
	solo := repeat(MinLoadSamples, call(100, 2000, 1))
	few := repeat(MinLoadSamples-1, call(200, 5000, 3))

	cases := map[string][]bus.SchedulerPerformance{
		"too few solo calls": {entry(gpu, small, append(repeat(MinLoadSamples-1, call(100, 2000, 1)),
			repeat(MinLoadSamples, call(200, 5000, 3))...)...)},
		"too few calls at the level": {entry(gpu, small, append(solo, few...)...)},
		"another server":             {entry("other", small, append(solo, repeat(MinLoadSamples, call(200, 5000, 3))...)...)},
		"no calibrated speeds":       {entry(gpu, big, append(solo, repeat(MinLoadSamples, call(200, 5000, 3))...)...)},
	}
	for name, bad := range map[string]bus.SchedulerPerformanceSamplesElem{
		"cold": cold, "failed": failed, "probe": probe, "empty": empty, "no duration": noDuration, "before": before} {
		bad.ActiveCalls = 3
		if bad.At > 1 {
			bad.At = 200
		}
		cases["only calls that tell nothing at the level: "+name] = []bus.SchedulerPerformance{
			entry(gpu, small, append(append([]bus.SchedulerPerformanceSamplesElem{}, solo...), repeat(MinLoadSamples, bad)...)...)}
	}
	for name, entries := range cases {
		if got := Contention(entries, gpu, 10, calibrated); len(got) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := Contention(nil, gpu, 0, calibrated); len(got) != 0 {
		t.Errorf("no history: %+v", got)
	}
	if got := Contention(cases["too few solo calls"], gpu, 0, map[string]Speeds{small: {}}); len(got) != 0 {
		t.Errorf("speeds of zero: %+v", got)
	}
}

func TestContention_aFactorNeverFallsBelowALowerLevelsAndNeverBelowOne(t *testing.T) {
	samples := slices.Concat(
		repeat(6, call(100, 2000, 1)),
		repeat(6, call(200, 3000, 2)),
		repeat(6, call(300, 2400, 3)),
		repeat(6, call(400, 1600, 6)),
	)

	got := Contention([]bus.SchedulerPerformance{entry(gpu, small, samples...)}, gpu, 0, calibrated)[small]

	for _, f := range got {
		if f.Factor < 1.5 {
			t.Errorf("a higher level published %+v below the lower level's 1.5", f)
		}
	}
	if len(got) == 0 || got[0].ActiveCalls != 2 || got[0].Factor != 1.5 {
		t.Errorf("got %+v", got)
	}
}
