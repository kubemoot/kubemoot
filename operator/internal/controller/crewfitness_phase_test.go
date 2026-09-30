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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
)

func TestDerivePhaseFromAssertions(t *testing.T) {
	pass := []kubemootv1alpha1.AssertionResult{{Raw: "a", Passed: true}}
	mixed := []kubemootv1alpha1.AssertionResult{{Raw: "a", Passed: true}, {Raw: "b", Passed: false}}
	cases := []struct {
		name       string
		assertions []kubemootv1alpha1.AssertionResult
		complete   bool
		want       kubemootv1alpha1.CrewFitnessPhase
	}{
		{"empty+complete -> Passed", nil, true, kubemootv1alpha1.CrewFitnessPhasePassed},
		{"empty+incomplete -> Error", nil, false, kubemootv1alpha1.CrewFitnessPhaseError},
		{"all passed -> Passed", pass, true, kubemootv1alpha1.CrewFitnessPhasePassed},
		{"any failed -> Failed", mixed, true, kubemootv1alpha1.CrewFitnessPhaseFailed},
	}
	for _, tc := range cases {
		if got := derivePhaseFromAssertions(tc.assertions, tc.complete); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestExtractFailureMessage(t *testing.T) {
	withCond := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Message: "pod OOMKilled"},
	}}}
	if got := extractFailureMessage(withCond); got != "pod OOMKilled" {
		t.Errorf("expected the JobFailed message, got %q", got)
	}
	if got := extractFailureMessage(&batchv1.Job{}); got != "Job failed" {
		t.Errorf("expected the default message, got %q", got)
	}
}

func TestDeterminePhase(t *testing.T) {
	r := &CrewFitnessReconciler{}
	// A failed Job with no assertions -> Error carrying the job's failure message.
	cf := &kubemootv1alpha1.CrewFitness{}
	job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Message: "boom"},
	}}}
	r.determinePhase(cf, job, false, true)
	if cf.Status.Phase != kubemootv1alpha1.CrewFitnessPhaseError || cf.Status.Error != "boom" {
		t.Errorf("failed/no-assertions: phase=%v err=%q", cf.Status.Phase, cf.Status.Error)
	}
	// A complete Job with passing assertions -> Passed.
	cf2 := &kubemootv1alpha1.CrewFitness{Status: kubemootv1alpha1.CrewFitnessStatus{
		Assertions: []kubemootv1alpha1.AssertionResult{{Raw: "a", Passed: true}},
	}}
	r.determinePhase(cf2, &batchv1.Job{}, true, false)
	if cf2.Status.Phase != kubemootv1alpha1.CrewFitnessPhasePassed {
		t.Errorf("complete/passing: phase=%v", cf2.Status.Phase)
	}
}
