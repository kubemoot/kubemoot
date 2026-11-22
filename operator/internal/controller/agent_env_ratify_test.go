/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"testing"

	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func ratifyTestArchetype(name string, ratify *kubemootv1alpha1.ArchetypeRatify) *kubemootv1alpha1.MootArchetype {
	a := reviewTestArchetype(name, nil)
	a.Spec.Ratify = ratify
	return a
}

func TestRatifyEnvVars_DefaultIsOff(t *testing.T) {
	for name, block := range map[string]*kubemootv1alpha1.ArchetypeRatify{
		"no block":        nil,
		"empty block":     {},
		"explicit false":  {Enabled: ptr.To(false)},
		"rounds only set": {MaxRounds: ptr.To(int32(3))},
	} {
		got := ratifyEnvVars(block)
		if v, ok := envValue(got, envRatifyEnabled); !ok || v != valueFalse {
			t.Fatalf("%s: want %s=false, got %v", name, envRatifyEnabled, got)
		}
	}
}

func TestRatifyEnvVars_ExplicitTrueTurnsItOn(t *testing.T) {
	got := ratifyEnvVars(&kubemootv1alpha1.ArchetypeRatify{Enabled: ptr.To(true)})
	if v, ok := envValue(got, envRatifyEnabled); !ok || v != valueTrue {
		t.Fatalf("want %s=true, got %v", envRatifyEnabled, got)
	}
}

func TestRatifyEnvVars_EveryFieldBecomesAVariable(t *testing.T) {
	got := ratifyEnvVars(&kubemootv1alpha1.ArchetypeRatify{
		Enabled: ptr.To(false), MaxRounds: ptr.To(int32(3)), Roster: kubemootv1alpha1.RatifyRosterCrew})
	enabled, _ := envValue(got, envRatifyEnabled)
	rounds, _ := envValue(got, envRatifyMaxRounds)
	roster, _ := envValue(got, envRatifyRoster)
	if len(got) != 3 || enabled != valueFalse || rounds != "3" || roster != string(kubemootv1alpha1.RatifyRosterCrew) {
		t.Fatalf("unexpected variables: %v", got)
	}
}

func TestCoordinatorRatifyEnvVars_FollowsTheCrewsArchetype(t *testing.T) {
	r := reviewTestReconciler(t,
		ratifyTestArchetype(defaultArchetypeName, &kubemootv1alpha1.ArchetypeRatify{MaxRounds: ptr.To(int32(2))}),
		ratifyTestArchetype(reviewTestPanel, &kubemootv1alpha1.ArchetypeRatify{Enabled: ptr.To(false)}),
		reviewTestPolicyFor(reviewTestPanel),
	)
	got := r.coordinatorRatifyEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName))
	if len(got) != 1 || got[0].Name != envRatifyEnabled || got[0].Value != valueFalse {
		t.Fatalf("crew on the panel archetype: want ratify off, got %v", got)
	}
	got = r.coordinatorRatifyEnvVars(context.Background(), reviewTestCoordinatorOf(reviewTestOtherCrew))
	rounds, _ := envValue(got, envRatifyMaxRounds)
	enabled, _ := envValue(got, envRatifyEnabled)
	if len(got) != 2 || rounds != "2" || enabled != valueFalse {
		t.Fatalf("crew without a policy uses the default archetype: want maxRounds=2 and ratify off, got %v", got)
	}
}

func TestCoordinatorRatifyEnvVars_MissingArchetypeSetsNothing(t *testing.T) {
	r := reviewTestReconciler(t)
	if got := r.coordinatorRatifyEnvVars(context.Background(), reviewTestCoordinatorOf(testCrewName)); len(got) != 0 {
		t.Fatalf("no archetype installed: want no variables, got %v", got)
	}
}
