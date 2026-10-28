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
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

const (
	agreeNS   = "agree-ns"
	agreeCrew = "agree-crew"
)

// envQueryEndpoints are the query endpoints an agent's env lists: every RAG source
// endpoint and the resume search endpoint.
func envQueryEndpoints(env map[string]string) []string {
	var out []string
	for name, value := range env {
		ragEndpoint := strings.HasPrefix(name, "KUBEMOOT_RAG_SOURCES_") && strings.HasSuffix(name, "__ENDPOINT")
		if ragEndpoint || name == "KUBEMOOT_RESUME_SEARCH_ENDPOINT" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func destKeys(dests []netpol.Dest) []string {
	out := make([]string, len(dests))
	for i, d := range dests {
		out[i] = fmt.Sprintf("%+v", d)
	}
	sort.Strings(out)
	return out
}

// TestEgressQueryServicesAgreeWithEnv fails if the endpoints an agent's env lists and the
// destinations its egress rule allows ever differ: same agent, same reconciler.
func TestEgressQueryServicesAgreeWithEnv(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	off := false
	cases := map[string]kubemootv1alpha1.AgentSpec{
		"coordinator with sources": {DiscussRole: roleCoordinator,
			RAGSources: []kubemootv1alpha1.RAGSourceRef{{Name: npDocs, Priority: 1}, {Name: "kb"}}},
		"coordinator alone":  {DiscussRole: roleCoordinator},
		"specialist sources": {RAGSources: []kubemootv1alpha1.RAGSourceRef{{Name: npDocs}}},
		"nothing":            {},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			agent := &kubemootv1alpha1.Agent{
				ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: agreeNS, Labels: map[string]string{labelCrew: agreeCrew}},
				Spec:       spec,
			}
			// "kb" has its query service switched off: neither side may list it.
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				&kubemootv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: "kb", Namespace: agreeNS},
					Spec: kubemootv1alpha1.RAGSourceSpec{QueryService: &kubemootv1alpha1.QueryServiceConfig{Enabled: &off}}},
			).Build()
			r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache(), NetPol: &NetPolicySync{Client: cli, Scheme: scheme}}
			ctx := context.Background()

			env := map[string]string{}
			for _, e := range r.buildEnvVars(ctx, agent, &modelPick{}, &modelPick{}, 8080) {
				env[e.Name] = e.Value
			}
			fromEnv := envQueryEndpoints(env)
			listed := r.queryServiceEndpoints(ctx, agent)
			sort.Strings(listed)
			if !reflect.DeepEqual(append([]string{}, fromEnv...), append([]string{}, listed...)) {
				t.Fatalf("env lists %v, egress is built from %v", fromEnv, listed)
			}

			want := make([]netpol.Dest, 0, len(fromEnv))
			for _, endpoint := range fromEnv {
				want = append(want, netpol.DestsFromURL(endpoint)...)
			}
			got, err := r.queryServiceDests(ctx, agent)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(destKeys(want), destKeys(got)) {
				t.Fatalf("env endpoints allow %v, egress allows %v", destKeys(want), destKeys(got))
			}
		})
	}
}
