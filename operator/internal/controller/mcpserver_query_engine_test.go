/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const duckdbEngine = string(kubemootv1alpha1.WorkspaceQueryEngineDuckDB)

func configWithQueryEngine(engine kubemootv1alpha1.WorkspaceQueryEngine) *kubemootv1alpha1.KubemootConfig {
	cfg := configWithArtifactAccess(artifactAccessV1)
	cfg.Spec.Workspace = &kubemootv1alpha1.WorkspaceConfig{QueryEngine: engine}
	return cfg
}

func queryEngineEnv(env []corev1.EnvVar) (string, bool) {
	for _, e := range env {
		if e.Name == workspaceQueryEngineEnv {
			return e.Value, true
		}
	}
	return "", false
}

func TestWithQueryEngineEnv(t *testing.T) {
	plain := readOpsServer()

	for _, engine := range []kubemootv1alpha1.WorkspaceQueryEngine{"", kubemootv1alpha1.WorkspaceQueryEngineNone} {
		if got := withQueryEngineEnv(plain, engine); got != plain {
			t.Errorf("engine %q must leave the server untouched", engine)
		}
	}

	want := map[kubemootv1alpha1.WorkspaceQueryEngine]string{
		kubemootv1alpha1.WorkspaceQueryEngineSQLite: string(kubemootv1alpha1.WorkspaceQueryEngineSQLite),
		kubemootv1alpha1.WorkspaceQueryEngineDuckDB: duckdbEngine,
	}
	for engine, value := range want {
		got := withQueryEngineEnv(plain, engine)
		if v, ok := queryEngineEnv(got.Spec.Env); !ok || v != value {
			t.Errorf("engine %q must set %s=%s, env = %v", engine, workspaceQueryEngineEnv, value, got.Spec.Env)
		}
	}
	if _, ok := queryEngineEnv(plain.Spec.Env); ok {
		t.Error("the server the user owns must not be changed")
	}

	assertServerOwnEngineWins(t)
}

func TestEffectiveQueryEngine(t *testing.T) {
	cases := map[kubemootv1alpha1.WorkspaceQueryEngine]kubemootv1alpha1.WorkspaceQueryEngine{
		"": kubemootv1alpha1.WorkspaceQueryEngineSQLite,
		kubemootv1alpha1.WorkspaceQueryEngineNone:   kubemootv1alpha1.WorkspaceQueryEngineNone,
		kubemootv1alpha1.WorkspaceQueryEngineSQLite: kubemootv1alpha1.WorkspaceQueryEngineSQLite,
		kubemootv1alpha1.WorkspaceQueryEngineDuckDB: kubemootv1alpha1.WorkspaceQueryEngineDuckDB,
	}
	for in, want := range cases {
		if got := effectiveQueryEngine(in); got != want {
			t.Errorf("effectiveQueryEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveReadsAnUnsetEngineAsSQLite(t *testing.T) {
	cfg := configWithQueryEngine("")
	r := &MCPServerReconciler{Client: fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(cfg).Build()}
	got, err := r.resolveMCPServerImage(context.Background(), readOpsServer())
	if err != nil || got.QueryEngine != kubemootv1alpha1.WorkspaceQueryEngineSQLite {
		t.Fatalf("unset engine resolved to %q, err %v", got.QueryEngine, err)
	}
}

func TestResolveCarriesTheQueryEngineOnlyForArtifactAccess(t *testing.T) {
	cfg := configWithQueryEngine(kubemootv1alpha1.WorkspaceQueryEngineDuckDB)
	r := &MCPServerReconciler{Client: fake.NewClientBuilder().WithScheme(gatewayScheme(t)).WithObjects(cfg).Build()}

	got, err := r.resolveMCPServerImage(context.Background(), readOpsServer())
	if err != nil || got.QueryEngine != kubemootv1alpha1.WorkspaceQueryEngineDuckDB {
		t.Fatalf("artifactAccess: engine %q err %v", got.QueryEngine, err)
	}
	own := &kubemootv1alpha1.MCPServer{Spec: kubemootv1alpha1.MCPServerSpec{Image: specImage}}
	got, err = r.resolveMCPServerImage(context.Background(), own)
	if err != nil || got.QueryEngine != "" {
		t.Fatalf("a server with its own image must get no engine: %q %v", got.QueryEngine, err)
	}
}

func deploymentEnv(t *testing.T, cli client.Client) []corev1.EnvVar {
	t.Helper()
	dep := &appsv1.Deployment{}
	if err := cli.Get(context.Background(), types.NamespacedName{Name: readOpsName, Namespace: testNS1}, dep); err != nil {
		t.Fatalf("get Deployment: %v", err)
	}
	return dep.Spec.Template.Spec.Containers[0].Env
}

func TestReconcileSetsTheQueryEngineOnTheDeployment(t *testing.T) {
	cases := []struct {
		name   string
		engine kubemootv1alpha1.WorkspaceQueryEngine
		want   string
		set    bool
	}{
		{"sqlite", kubemootv1alpha1.WorkspaceQueryEngineSQLite, string(kubemootv1alpha1.WorkspaceQueryEngineSQLite), true},
		{"duckdb", kubemootv1alpha1.WorkspaceQueryEngineDuckDB, duckdbEngine, true},
		{coreTestNone, kubemootv1alpha1.WorkspaceQueryEngineNone, "", false},
		{"unset", "", string(kubemootv1alpha1.WorkspaceQueryEngineSQLite), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, cli, req := newReadOpsReconciler(t, configWithQueryEngine(tc.engine))
			reconcileTwice(t, r, req)
			v, ok := queryEngineEnv(deploymentEnv(t, cli))
			if ok != tc.set || v != tc.want {
				t.Errorf("env %s = %q (set %v), want %q (set %v)", workspaceQueryEngineEnv, v, ok, tc.want, tc.set)
			}
		})
	}
}

// assertServerOwnEngineWins checks that a server which sets either engine variable keeps it and
// gains no second one.
func assertServerOwnEngineWins(t *testing.T) {
	t.Helper()
	for _, name := range []string{workspaceQueryEngineEnv, artifactQueryEngineEnv} {
		own := readOpsServer()
		own.Spec.Env = []corev1.EnvVar{{Name: name, Value: string(kubemootv1alpha1.WorkspaceQueryEngineNone)}}
		got := withQueryEngineEnv(own, kubemootv1alpha1.WorkspaceQueryEngineDuckDB).Spec.Env
		if len(got) != 1 || got[0].Name != name || got[0].Value != string(kubemootv1alpha1.WorkspaceQueryEngineNone) {
			t.Errorf("a server that sets %s itself must keep it and gain no second engine variable, got %v", name, got)
		}
	}
}
