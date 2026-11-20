/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// defaulted is a retention block as the API server fills it from the CRD defaults.
func defaulted() *kubemootv1alpha1.CrewRetention {
	return &kubemootv1alpha1.CrewRetention{
		Schedule:      DefaultSchedule,
		PurgeUnused:   text90d,
		Conversations: &kubemootv1alpha1.ConversationRetention{Window: "30d"},
		Memory:        &kubemootv1alpha1.MemoryRetention{},
	}
}

func crewWith(r *kubemootv1alpha1.CrewRetention) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "crew"},
		Spec:       kubemootv1alpha1.CrewSpec{Retention: r},
	}
}

func TestResolveDefaults(t *testing.T) {
	p, err := Resolve(crewWith(defaulted()))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enforced() || p.Schedule.String() != DefaultSchedule {
		t.Errorf("schedule: %+v", p.Schedule)
	}
	if p.Conversations != 30*day {
		t.Errorf("conversations window %v", p.Conversations)
	}
	if p.Memory.IsNever() || p.Memory.Value() != 90*day {
		t.Errorf("memory inherits the crew-wide purgeUnused, got %v", p.Memory)
	}
	if p.Other != 0 {
		t.Errorf("other has no default window, got %v", p.Other)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("unexpected warnings %v", p.Warnings)
	}
}

func TestResolveUndeclaredKeepsEverything(t *testing.T) {
	for _, r := range []*kubemootv1alpha1.CrewRetention{nil, {}} {
		p, err := Resolve(crewWith(r))
		if err != nil || p.Enforced() {
			t.Errorf("a crew without defaulted retention removes nothing: %+v %v", p, err)
		}
	}
}

func TestResolveMemoryNeverAndOverrides(t *testing.T) {
	r := defaulted()
	r.Memory.PurgeUnused = Never
	p, err := Resolve(crewWith(r))
	if err != nil || !p.Memory.IsNever() {
		t.Fatalf("memory never: %+v %v", p, err)
	}

	r = defaulted()
	r.Memory.PurgeUnused = "1y"
	if p, _ = Resolve(crewWith(r)); p.Memory.Value() != 365*day {
		t.Errorf("memory own purgeUnused: %v", p.Memory)
	}

	r = defaulted()
	r.PurgeUnused = Never
	if p, _ = Resolve(crewWith(r)); !p.Memory.IsNever() || p.Conversations != 30*day {
		t.Errorf("crew-wide never leaves memory permanent and conversations on their window: %+v", p)
	}
}

func TestResolveConversationsTakeTheShorterOfWindowAndPurgeUnused(t *testing.T) {
	r := defaulted()
	r.PurgeUnused = "7d"
	if p, _ := Resolve(crewWith(r)); p.Conversations != 7*day {
		t.Errorf("want 7d, got %v", p.Conversations)
	}
	r = defaulted()
	r.Conversations.Window = "1w"
	if p, _ := Resolve(crewWith(r)); p.Conversations != 7*day {
		t.Errorf("want 1w, got %v", p.Conversations)
	}
}

func TestResolveLegacyTTLDaysOverridesMemory(t *testing.T) {
	c := crewWith(defaulted())
	c.Spec.Memory = &kubemootv1alpha1.CrewMemoryConfig{TTLDays: 10}
	if p, _ := Resolve(c); p.Memory.Value() != 10*day {
		t.Errorf("ttlDays: %v", p.Memory)
	}
}

func TestResolveOtherWindow(t *testing.T) {
	r := defaulted()
	r.Other = &kubemootv1alpha1.OtherRetention{Window: text36h}
	if p, _ := Resolve(crewWith(r)); p.Other != 36*time.Hour {
		t.Errorf("other: %v", p.Other)
	}
}

func TestResolveRejectsInvalidValuesTogether(t *testing.T) {
	r := defaulted()
	r.Schedule = "whenever"
	r.PurgeUnused = "1d1y"
	r.Conversations.Window = Never
	r.Memory.PurgeUnused = "forever"
	r.Other = &kubemootv1alpha1.OtherRetention{Window: "6y"}
	_, err := Resolve(crewWith(r))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"schedule", "retention.purgeUnused", "retention.conversations.window", "retention.memory.purgeUnused", "retention.other.window"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestResolveAcceptsTheLongestWindow(t *testing.T) {
	r := defaulted()
	r.PurgeUnused = Never
	r.Conversations.Window = kubemootv1alpha1.RetentionDuration(MaxWindowText)
	if _, err := Resolve(crewWith(r)); err != nil {
		t.Errorf("the longest window is accepted: %v", err)
	}
	r.Conversations.Window = "5y1d"
	if _, err := Resolve(crewWith(r)); err == nil {
		t.Error("a window past the longest is refused")
	}
}

func TestResolveWarnsWhenFinerThanTheSchedule(t *testing.T) {
	r := defaulted()
	r.Conversations.Window = "12h"
	r.Memory.PurgeUnused = "6h"
	r.Other = &kubemootv1alpha1.OtherRetention{Window: "1h"}
	p, err := Resolve(crewWith(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 3 {
		t.Fatalf("want a warning each for conversations, memory and other, got %v", p.Warnings)
	}
	r.Schedule = "@hourly"
	if p, _ = Resolve(crewWith(r)); len(p.Warnings) != 0 {
		t.Errorf("an hourly schedule resolves these windows: %v", p.Warnings)
	}
	r.Memory.PurgeUnused = Never
	r.Schedule = DefaultSchedule
	r.Conversations.Window = "1d"
	r.Other = nil
	if p, _ = Resolve(crewWith(r)); len(p.Warnings) != 0 {
		t.Errorf("never and a window equal to the schedule do not warn: %v", p.Warnings)
	}
}
