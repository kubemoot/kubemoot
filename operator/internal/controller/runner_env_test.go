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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func toMap(env []corev1.EnvVar) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

// TestBuildRunnerEnv_SuiteIterationGetsTranscriptCoords verifies a suite child
// (carrying the suite labels) gets the NATS transcript coordinates with the
// correct key, while a standalone CrewFitness does not.
func TestBuildRunnerEnv_SuiteIterationGetsTranscriptCoords(t *testing.T) {
	t.Setenv("NATS_URL", "nats://nats.nats:4222")

	suiteChild := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-abc12345-s1-i3",
			Namespace: "crew-homelab-pilot",
			Labels: map[string]string{
				suiteOwnerLabel:          "baseline-n10",
				suiteRunIDLabel:          "abc12345",
				suiteScriptIndexLabel:    "1",
				suiteIterationIndexLabel: "3",
			},
		},
	}
	env := buildRunnerEnv(suiteChild, "http://gw:8080/api/v1/discussions/crew", "test.adl", "run-abc12345-s1-i3-job")
	m := toMap(env)
	if m["NATS_URL"] != "nats://nats.nats:4222" {
		t.Errorf("NATS_URL not wired: %q", m["NATS_URL"])
	}
	if m["TRANSCRIPT_BUCKET"] != FitnessArtifactsBucket {
		t.Errorf("TRANSCRIPT_BUCKET = %q, want %q", m["TRANSCRIPT_BUCKET"], FitnessArtifactsBucket)
	}
	wantKey := "crew-homelab-pilot/baseline-n10/abc12345/s1-i3.json"
	if m["TRANSCRIPT_KEY"] != wantKey {
		t.Errorf("TRANSCRIPT_KEY = %q, want %q", m["TRANSCRIPT_KEY"], wantKey)
	}
	// Base env still present.
	if m["DISCUSSION_ENDPOINT"] == "" || m["TEST_FILE"] != "/tests/test.adl" {
		t.Errorf("base env missing: %+v", m)
	}
}

func TestBuildRunnerEnv_StandaloneSkipsTranscript(t *testing.T) {
	t.Setenv("NATS_URL", "nats://nats.nats:4222")
	standalone := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{Name: "adhoc", Namespace: "crew-test"},
	}
	m := toMap(buildRunnerEnv(standalone, "http://gw", "t.adl", "adhoc-job"))
	if _, ok := m["TRANSCRIPT_KEY"]; ok {
		t.Errorf("standalone CrewFitness should NOT get TRANSCRIPT_KEY: %+v", m)
	}
}

func TestBuildRunnerEnv_NoNatsUrlSkipsTranscript(t *testing.T) {
	t.Setenv("NATS_URL", "")
	suiteChild := &kubemootv1alpha1.CrewFitness{
		ObjectMeta: metav1.ObjectMeta{
			Name: "run-x-s0-i1", Namespace: "crew-test",
			Labels: map[string]string{suiteOwnerLabel: "s", suiteRunIDLabel: "x"},
		},
	}
	m := toMap(buildRunnerEnv(suiteChild, "http://gw", "t.adl", "j"))
	if _, ok := m["TRANSCRIPT_KEY"]; ok {
		t.Errorf("no NATS_URL → no transcript coords: %+v", m)
	}
}
