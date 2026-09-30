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
	"errors"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// remainingKeys returns the sorted keys still present in a fakeStore (objectStore
// fake defined in report_server_test.go).
func remainingKeys(s fakeStore) []string {
	out := make([]string, 0, len(s.objs))
	for k := range s.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPurgeArtifactsDeletesOnlyUnderPrefix: a suite's XLSX, transcripts, and
// deferred-score sidecar (all under "<ns>/<name>/") are deleted, while another
// suite's artifacts, a name-prefix-collision suite, and a different namespace are
// left untouched. This is the purge the delete finalizer relies on.
func TestPurgeArtifactsDeletesOnlyUnderPrefix(t *testing.T) {
	store := fakeStore{objs: map[string][]byte{
		"crew-pilot/adl-run/r1.xlsx":                    {1}, // XLSX (ns/name/runID.xlsx)
		"crew-pilot/adl-run/r1/s0-i1.json":              {1}, // transcript
		"crew-pilot/adl-run/r1/deferred-scores-v2.json": {1}, // deferred-score sidecar
		"crew-pilot/adl-run/r2.xlsx":                    {1}, // a re-run (different runID) -> also purged
		"crew-pilot/other-run/r1.xlsx":                  {1}, // different suite -> keep
		"crew-pilot/adl-run-2/r1.xlsx":                  {1}, // name-prefix collision -> keep
		"crew-other/adl-run/r1.xlsx":                    {1}, // different namespace -> keep
	}}

	n, err := purgeArtifacts(store, FitnessArtifactsBucket, "crew-pilot/adl-run/")
	if err != nil {
		t.Fatalf("purgeArtifacts: %v", err)
	}
	if n != 4 {
		t.Errorf("deleted count: got %d, want 4", n)
	}
	want := []string{
		"crew-other/adl-run/r1.xlsx",
		"crew-pilot/adl-run-2/r1.xlsx",
		"crew-pilot/other-run/r1.xlsx",
	}
	if got := remainingKeys(store); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("remaining objects: got %v, want %v", got, want)
	}
}

// TestPurgeArtifactsReportsDeleteError: a delete failure surfaces as an error so
// the finalizer logs it (rather than silently claiming success).
func TestPurgeArtifactsReportsDeleteError(t *testing.T) {
	boom := errors.New("nats unavailable")
	store := fakeStore{objs: map[string][]byte{"crew-pilot/adl-run/r1.xlsx": {1}}, delErr: boom}
	n, err := purgeArtifacts(store, FitnessArtifactsBucket, "crew-pilot/adl-run/")
	if err == nil {
		t.Fatal("expected an error when DeleteObject fails")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error should wrap the delete failure, got %v", err)
	}
	if n != 0 {
		t.Errorf("deleted count on failure: got %d, want 0", n)
	}
}

// TestFinalizeSuiteRemovesFinalizer: deleting a suite (DeletionTimestamp set)
// runs finalizeSuite, which removes the finalizer so the CR is garbage-collected.
// NATSPublisher is nil here (purge no-ops); the purge logic itself is covered by
// TestPurgeArtifactsDeletesOnlyUnderPrefix.
func TestFinalizeSuiteRemovesFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "adl-run",
			Namespace:  "crew-pilot",
			Finalizers: []string{crewFitnessSuiteFinalizer},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(suite).Build()
	ctx := context.Background()

	// Delete sets DeletionTimestamp; the finalizer keeps the object around.
	if err := cli.Delete(ctx, suite); err != nil {
		t.Fatalf("delete: %v", err)
	}
	key := client.ObjectKey{Name: "adl-run", Namespace: "crew-pilot"}
	pending := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cli.Get(ctx, key, pending); err != nil {
		t.Fatalf("get after delete (should still exist via finalizer): %v", err)
	}
	if pending.DeletionTimestamp.IsZero() {
		t.Fatal("expected DeletionTimestamp to be set")
	}

	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme} // NATSPublisher nil
	if _, err := r.finalizeSuite(ctx, pending); err != nil {
		t.Fatalf("finalizeSuite: %v", err)
	}

	out := &kubemootv1alpha1.CrewFitnessSuite{}
	err := cli.Get(ctx, key, out)
	if apierrors.IsNotFound(err) {
		return // finalizer removed -> CR garbage-collected: correct
	}
	if err != nil {
		t.Fatalf("get after finalize: %v", err)
	}
	if controllerutil.ContainsFinalizer(out, crewFitnessSuiteFinalizer) {
		t.Error("finalizer was not removed; CR deletion stays wedged")
	}
}
