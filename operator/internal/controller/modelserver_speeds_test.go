/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"
)

func TestModelSpeeds(t *testing.T) {
	cal := &aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{
		{Model: calTestModelA, Fits: true, LoadMs: 2892, PromptTokensPerSecond: 15162, GenerateTokensPerSecond: 235},
		{Model: calTestModelB, Fits: true, GenerateTokensPerSecond: 90},
		{Model: "big:32b", Fits: false, LoadMs: 29527},
		{Model: "unmeasured:1b", Fits: true},
	}}
	observed := []perf.Observation{
		{Model: calTestModelA, SoloSamples: perf.MinSamples, SoloTokensPerSecond: 220},
		{Model: calTestModelB, SoloSamples: perf.MinSamples - 1, SoloTokensPerSecond: 10},
	}
	got := modelSpeeds(cal, observed, map[string][]perf.LoadFactor{
		calTestModelA: {{ActiveCalls: 3, Factor: 1.4, Samples: 9}}})
	if len(got) != 2 {
		t.Fatalf("only fitting, measured models: %+v", got)
	}
	a := got[calTestModelA]
	checkSpeedOfA(t, a)
	if b := got[calTestModelB]; b.ObservedGenerateTokensPerSecond != 0 || b.ObservedSamples != 0 {
		t.Errorf("too few observed calls should not be published: %+v", b)
	}
	if modelSpeeds(nil, observed, nil) != nil {
		t.Error("no calibration, no speeds")
	}
	if modelSpeeds(&aiv1alpha1.ServerCalibration{}, nil, nil) != nil {
		t.Error("a calibration with no models publishes no speeds")
	}
}

// The operator's server state decodes into the bus contract's type, so the field
// names agents read match the schema.
func TestServerStateModelSpeedsMatchTheContract(t *testing.T) {
	state := ServerState{Name: calTestServer, ModelSpeeds: map[string]ModelSpeed{
		calTestModelA: {LoadMs: 1, PromptTokensPerSecond: 2, GenerateTokensPerSecond: 3,
			ObservedGenerateTokensPerSecond: 4, ObservedSamples: 5,
			LoadFactors: []perf.LoadFactor{{ActiveCalls: 5, Factor: 1.25, Samples: 9}}},
	}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var contract bus.ServerState
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	s := contract.ModelSpeeds[calTestModelA]
	if s.LoadMs != 1 || s.PromptTokensPerSecond != 2 || s.GenerateTokensPerSecond != 3 ||
		s.ObservedGenerateTokensPerSecond != 4 || s.ObservedSamples != 5 {
		t.Errorf("decoded: %+v from %s", s, raw)
	}
	checkDecodedLoadFactors(t, s.LoadFactors, raw)
}

// checkDecodedLoadFactors fails unless the contract's load factors are the one the operator published.
func checkDecodedLoadFactors(t *testing.T, got []bus.ServerStateModelSpeedsValueLoadFactorsElem, raw []byte) {
	t.Helper()
	if len(got) != 1 || got[0].ActiveCalls != 5 || got[0].Factor != 1.25 || got[0].Samples != 9 {
		t.Errorf("load factors decoded: %+v from %s", got, raw)
	}
}

// checkSpeedOfA fails unless the speed is model A's calibrated and observed speeds, with the one load
// factor the test gave it.
func checkSpeedOfA(t *testing.T, speed ModelSpeed) {
	t.Helper()
	if speed.LoadMs != 2892 || speed.PromptTokensPerSecond != 15162 || speed.GenerateTokensPerSecond != 235 ||
		speed.ObservedGenerateTokensPerSecond != 220 || speed.ObservedSamples != perf.MinSamples {
		t.Errorf("%s: %+v", calTestModelA, speed)
	}
	if len(speed.LoadFactors) != 1 || speed.LoadFactors[0].Factor != 1.4 || speed.LoadFactors[0].ActiveCalls != 3 {
		t.Errorf("load factors: %+v", speed.LoadFactors)
	}
}
