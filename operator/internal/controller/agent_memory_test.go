/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

// TestApplyMemoryOverrides pins the override contract extracted from memoryEnvVars
// during the cognitive-complexity refactor: nil config leaves defaults untouched,
// the *bool fields honor an explicit false, and the int32 fields override only when
// positive (zero/unset never clobbers a default).
// memOverride is one applyMemoryOverrides outcome: the five values after the
// override is applied to the package defaults. Table cases compare against it.
type memOverride struct {
	enabled, verify     bool
	maxFacts, ttl, injt int32
}

// defaults captures the pre-override baseline each case starts from.
var memDefaults = memOverride{enabled: true, verify: true, maxFacts: 5000, ttl: 365, injt: 8}

// runMemOverride applies cfg to the given start state and returns the result,
// keeping the pointer plumbing out of each table case.
func runMemOverride(start memOverride, cfg *kubemootv1alpha1.CrewMemoryConfig) memOverride {
	got := start
	applyMemoryOverrides(cfg, &got.enabled, &got.verify, &got.maxFacts, &got.ttl, &got.injt)
	return got
}

func TestApplyMemoryOverrides(t *testing.T) {
	bptr := func(b bool) *bool { return &b }
	cases := []struct {
		name  string
		start memOverride
		cfg   *kubemootv1alpha1.CrewMemoryConfig
		want  memOverride
	}{
		{
			name:  "nil config leaves defaults",
			start: memDefaults,
			cfg:   nil,
			want:  memDefaults,
		},
		{
			name:  "explicit false honored, zero ints ignored",
			start: memDefaults,
			cfg: &kubemootv1alpha1.CrewMemoryConfig{
				Enabled:     bptr(false),
				VerifyOnAdd: bptr(false),
				// MaxFacts/TTLDays/InjectLimit left zero -> must NOT override
			},
			want: memOverride{enabled: false, verify: false, maxFacts: 5000, ttl: 365, injt: 8},
		},
		{
			name:  "positive ints override, nil bools untouched",
			start: memDefaults,
			cfg: &kubemootv1alpha1.CrewMemoryConfig{
				MaxFacts:    100,
				TTLDays:     30,
				InjectLimit: 3,
			},
			want: memOverride{enabled: true, verify: true, maxFacts: 100, ttl: 30, injt: 3},
		},
		{
			// Start disabled to prove an explicit true flips it (false -> true).
			name:  "explicit true on enabled",
			start: memOverride{enabled: false, verify: true, maxFacts: 5000, ttl: 365, injt: 8},
			cfg:   &kubemootv1alpha1.CrewMemoryConfig{Enabled: bptr(true)},
			want:  memDefaults,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runMemOverride(tc.start, tc.cfg); got != tc.want {
				t.Errorf("applyMemoryOverrides = %+v, want %+v", got, tc.want)
			}
		})
	}
}
