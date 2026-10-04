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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// crewProvenanceScheme builds a scheme with the Kubemoot CRDs registered.
func crewProvenanceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return scheme
}

func crewWithVersion(version string) *kubemootv1alpha1.Crew {
	labels := map[string]string{}
	if version != "" {
		labels[crewVersionLabel] = version
	}
	return &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: testCrewName, Namespace: testCrewNamespace, Labels: labels},
	}
}

func TestResolveCrewVersion(t *testing.T) {
	scheme := crewProvenanceScheme(t)

	t.Run("crew present with version label returns the version", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(crewWithVersion(testVersion142)).Build()
		if v := resolveCrewVersion(context.Background(), c, testCrewNamespace, testCrewName); v != testVersion142 {
			t.Fatalf("want 1.4.2, got %q", v)
		}
	})

	t.Run("crew absent returns empty (best-effort, never blocks)", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		if v := resolveCrewVersion(context.Background(), c, testCrewNamespace, "missing"); v != "" {
			t.Fatalf("want empty for missing crew, got %q", v)
		}
	})

	t.Run("empty crewRef returns empty without a lookup", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		if v := resolveCrewVersion(context.Background(), c, testCrewNamespace, ""); v != "" {
			t.Fatalf("want empty for blank crewRef, got %q", v)
		}
	})

	t.Run("crew present but no version label returns empty", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(crewWithVersion("")).Build()
		if v := resolveCrewVersion(context.Background(), c, testCrewNamespace, testCrewName); v != "" {
			t.Fatalf("want empty for version-less crew, got %q", v)
		}
	})
}

func TestStampCrewVersion(t *testing.T) {
	scheme := crewProvenanceScheme(t)
	mkSuite := func(labels map[string]string) *kubemootv1alpha1.CrewFitnessSuite {
		return &kubemootv1alpha1.CrewFitnessSuite{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: testCrewNamespace, Labels: labels},
			Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName},
		}
	}

	t.Run("resolves from the Crew CR and stamps the in-memory suite", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(crewWithVersion(testVersion142)).Build()
		suite := mkSuite(nil)
		stampCrewVersion(context.Background(), c, suite)
		if suite.Labels[crewVersionLabel] != testVersion142 {
			t.Fatalf("want suite stamped 1.4.2, got %q", suite.Labels[crewVersionLabel])
		}
	})

	t.Run("crew absent leaves the suite unchanged", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		suite := mkSuite(nil)
		stampCrewVersion(context.Background(), c, suite)
		if _, ok := suite.Labels[crewVersionLabel]; ok {
			t.Fatalf("suite must be unchanged when the crew has no version")
		}
	})

	t.Run("existing label is a no-op (idempotent, no overwrite)", func(t *testing.T) {
		// Crew CR says 2.0.0, but the suite already carries 1.0.0 - the short-circuit
		// must keep the pre-set value and not re-resolve.
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(crewWithVersion(testVersion200)).Build()
		suite := mkSuite(map[string]string{crewVersionLabel: testVersion100})
		stampCrewVersion(context.Background(), c, suite)
		if suite.Labels[crewVersionLabel] != testVersion100 {
			t.Fatalf("idempotency: want preserved 1.0.0, got %q", suite.Labels[crewVersionLabel])
		}
	})
}
