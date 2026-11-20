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

	"k8s.io/client-go/tools/events"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func retentionCrew(r *kubemootv1alpha1.CrewRetention) *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewSpec{Retention: r},
	}
}

func filledRetention() *kubemootv1alpha1.CrewRetention {
	return &kubemootv1alpha1.CrewRetention{
		Schedule: "@daily", PurgeUnused: "90d",
		Conversations: &kubemootv1alpha1.ConversationRetention{Window: "30d"},
		Memory:        &kubemootv1alpha1.MemoryRetention{},
	}
}

func TestReconcileRetentionValid(t *testing.T) {
	rec := events.NewFakeRecorder(5)
	r := &CrewReconciler{Recorder: rec}
	crew := retentionCrew(filledRetention())
	r.reconcileRetention(crew)
	c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeRetentionValid)
	if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reasonRetentionValid {
		t.Fatalf("condition: %+v", c)
	}
	if len(rec.Events) != 0 {
		t.Errorf("a valid policy sends no event: %v", <-rec.Events)
	}
}

func TestReconcileRetentionInvalidWarnsOnceAndNamesTheField(t *testing.T) {
	rec := events.NewFakeRecorder(5)
	r := &CrewReconciler{Recorder: rec}
	bad := filledRetention()
	bad.Conversations.Window = retentionNever
	crew := retentionCrew(bad)
	r.reconcileRetention(crew)
	c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeRetentionValid)
	if c == nil || c.Status != metav1.ConditionFalse || c.Reason != reasonRetentionInvalid {
		t.Fatalf("condition: %+v", c)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("want one warning event, got %d", len(rec.Events))
	}
	r.reconcileRetention(crew) // unchanged: no second event
	if len(rec.Events) != 1 {
		t.Errorf("the same problem was announced again: %d events", len(rec.Events))
	}
}

func TestReconcileRetentionWarnsWhenFinerThanTheSchedule(t *testing.T) {
	rec := events.NewFakeRecorder(5)
	r := &CrewReconciler{Recorder: rec}
	fine := filledRetention()
	fine.Conversations.Window = "6h"
	crew := retentionCrew(fine)
	r.reconcileRetention(crew)
	c := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeRetentionValid)
	if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reasonRetentionFinerThanSchedule {
		t.Fatalf("a finer duration is valid but reported: %+v", c)
	}
	if len(rec.Events) != 1 {
		t.Errorf("want one warning event, got %d", len(rec.Events))
	}
	// Fixing it clears the reason.
	crew.Spec.Retention.Conversations.Window = "30d"
	r.reconcileRetention(crew)
	if c = meta.FindStatusCondition(crew.Status.Conditions, conditionTypeRetentionValid); c.Reason != reasonRetentionValid {
		t.Errorf("reason after the fix: %s", c.Reason)
	}
}

func TestReconcileRetentionWithoutARecorder(t *testing.T) {
	r := &CrewReconciler{}
	bad := filledRetention()
	bad.Schedule = "whenever"
	r.reconcileRetention(retentionCrew(bad)) // must not panic
}

func TestMemoryTTLDays(t *testing.T) {
	set := func(mutate func(*kubemootv1alpha1.CrewRetention)) *kubemootv1alpha1.Crew {
		r := filledRetention()
		mutate(r)
		return retentionCrew(r)
	}
	cases := map[string]struct {
		crew *kubemootv1alpha1.Crew
		want int32
	}{
		"default 90d":                {retentionCrew(filledRetention()), 90},
		"never keeps all":            {set(func(r *kubemootv1alpha1.CrewRetention) { r.Memory.PurgeUnused = retentionNever }), 0},
		"rounds up":                  {set(func(r *kubemootv1alpha1.CrewRetention) { r.Memory.PurgeUnused = "36h" }), 2},
		"under a day is 1":           {set(func(r *kubemootv1alpha1.CrewRetention) { r.Memory.PurgeUnused = "1h" }), 1},
		"a year":                     {set(func(r *kubemootv1alpha1.CrewRetention) { r.Memory.PurgeUnused = "1y" }), 365},
		"invalid keeps all":          {set(func(r *kubemootv1alpha1.CrewRetention) { r.PurgeUnused = retentionBad }), 0},
		"unfilled keeps the current": {retentionCrew(nil), 365},
	}
	for name, tc := range cases {
		if got := memoryTTLDays(tc.crew, 365); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
	legacy := retentionCrew(filledRetention())
	legacy.Spec.Memory = &kubemootv1alpha1.CrewMemoryConfig{TTLDays: 7}
	if got := memoryTTLDays(legacy, 365); got != 7 {
		t.Errorf("the deprecated ttlDays still applies: %d", got)
	}
}

// Values the retention tests repeat.
const (
	retentionNever = "never"
	retentionBad   = "bad"
)
