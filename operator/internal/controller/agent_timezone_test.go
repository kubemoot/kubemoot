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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestDefaultTimeZoneEnv(t *testing.T) {
	cases := map[string]struct {
		zone string
		want string
	}{
		"a declared zone": {zone: "America/New_York", want: "America/New_York"},
		"trimmed":         {zone: " Europe/Paris ", want: "Europe/Paris"},
		"not declared":    {zone: "", want: ""},
		"blank":           {zone: "   ", want: ""},
	}
	for name, c := range cases {
		crew := &kubemootv1alpha1.Crew{Spec: kubemootv1alpha1.CrewSpec{DefaultTimeZone: c.zone}}
		env := defaultTimeZoneEnv(crew)
		if c.want == "" {
			if len(env) != 0 {
				t.Errorf("%s: got %v, want no variable", name, env)
			}
			continue
		}
		if len(env) != 1 || env[0].Name != envDefaultTimeZone || env[0].Value != c.want {
			t.Errorf("%s: got %v, want %s=%s", name, env, envDefaultTimeZone, c.want)
		}
	}
}

func TestDefaultTimeZoneEnvVarsReadsTheCrewOfTheNamespace(t *testing.T) {
	scheme := agentReconcileScheme(t)
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec:       kubemootv1alpha1.CrewSpec{DefaultTimeZone: "Asia/Tokyo"},
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(crew).Build()}
	if env := r.defaultTimeZoneEnvVars(context.Background(), "ns"); len(env) != 1 || env[0].Value != "Asia/Tokyo" {
		t.Errorf("crew with a zone: %v", env)
	}
	if env := r.defaultTimeZoneEnvVars(context.Background(), "other"); len(env) != 0 {
		t.Errorf("namespace without a crew: %v", env)
	}
}
