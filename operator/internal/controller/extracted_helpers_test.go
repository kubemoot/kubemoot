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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Fixture values for the tests of the helpers in this file.
const (
	ehPhaseMotion   = "motion"
	ehPhaseDebate   = "debate"
	ehArchetype     = "roberts"
	ehNamespace     = "tools"
	ehToolIndexName = "gw-tools"
	ehAgree         = "agree"
	ehConcern       = "concern"
	ehStandAside    = "stand_aside"
)

func TestPolicyPhaseValidationError(t *testing.T) {
	archetype := &kubemootv1alpha1.MootArchetype{}
	archetype.Spec.Phases = []kubemootv1alpha1.ArchetypePhase{{Name: ehPhaseMotion}, {Name: ehPhaseDebate}}

	known := &kubemootv1alpha1.CrewSchedulingPolicy{}
	known.Spec.Rules = []kubemootv1alpha1.SchedulingRule{{Phase: ehPhaseMotion}, {Phase: ehPhaseDebate}}
	if got := policyPhaseValidationError(known, archetype, ehArchetype); got != "" {
		t.Errorf("known phases: got %q, want empty", got)
	}

	unknown := &kubemootv1alpha1.CrewSchedulingPolicy{}
	unknown.Spec.Rules = []kubemootv1alpha1.SchedulingRule{{Phase: "omega"}, {Phase: ehPhaseMotion}, {Phase: "kappa"}}
	want := `phase(s) not in archetype "roberts" vocabulary: kappa, omega`
	if got := policyPhaseValidationError(unknown, archetype, ehArchetype); got != want {
		t.Errorf("unknown phases: got %q, want %q", got, want)
	}

	if got := policyPhaseValidationError(&kubemootv1alpha1.CrewSchedulingPolicy{}, archetype, ehArchetype); got != "" {
		t.Errorf("no rules: got %q, want empty", got)
	}
}

func TestPrometheusScalarValue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{"numeric sample", `{"status":"success","data":{"result":[{"value":[1,"42.5"]}]}}`, 42.5},
		{"not json", `<html>`, 0},
		{"query error", `{"status":"error","data":{"result":[{"value":[1,"7"]}]}}`, 0},
		{"no result", `{"status":"success","data":{"result":[]}}`, 0},
		{"short sample", `{"status":"success","data":{"result":[{"value":[1]}]}}`, 0},
		{"value not a string", `{"status":"success","data":{"result":[{"value":[1,7]}]}}`, 0},
		{"value not a number", `{"status":"success","data":{"result":[{"value":[1,"NaNish"]}]}}`, 0},
	}
	for _, tc := range cases {
		if got := prometheusScalarValue([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSanitizeK8sNameRules(t *testing.T) {
	cases := map[string]string{
		"Org/Repo_Name v1.2":           "org-repo-name-v1-2",
		"9lives":                       "mcp-9lives",
		"--edge--":                     "edge",
		"@@@":                          unnamedServerName,
		"café-server":                  "caf-server",
		strings.Repeat("a", 62) + "-b": strings.Repeat("a", 62),
	}
	for in, want := range cases {
		if got := sanitizeK8sName(in); got != want {
			t.Errorf("sanitizeK8sName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGatewayRegistrationURL(t *testing.T) {
	gw := &kubemootv1alpha1.MCPGateway{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: ehNamespace}}
	if got := gatewayRegistrationURL(gw, kubemootv1alpha1.ImplementationKubemoot, 8080); got != "http://gw.tools.svc:8080/admin/servers" {
		t.Errorf("kubemoot gateway URL = %q", got)
	}
	if got := gatewayRegistrationURL(gw, kubemootv1alpha1.ImplementationContextForge, 4444); got != "http://gw.tools.svc:4444/gateways" {
		t.Errorf("contextforge gateway URL = %q", got)
	}
}

func TestHTTPClientOrDefault(t *testing.T) {
	injected := &http.Client{}
	if got := (&MCPGatewayReconciler{HTTPClient: injected}).httpClientOrDefault(); got != injected {
		t.Error("expected the injected client")
	}
	if got := (&MCPGatewayReconciler{}).httpClientOrDefault(); got == nil || got.Timeout != 10*time.Second {
		t.Errorf("default client = %+v, want a 10s timeout", got)
	}
}

func TestSyncToolIndexStatus(t *testing.T) {
	ctx := context.Background()
	indexed := metav1.Now()

	notReady := &kubemootv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: ehToolIndexName}}
	notReady.Status.Phase = "Indexing"
	notReady.Status.LastJobName = "index-1"
	gw := &kubemootv1alpha1.MCPGateway{}
	if syncToolIndexStatus(ctx, gw, notReady) {
		t.Fatal("a RAGSource that is not ready must not report ready")
	}
	if gw.Status.CatalogSync.JobStatus != "Indexing" || gw.Status.CatalogSync.JobName != "index-1" {
		t.Errorf("not-ready sync status = %+v", gw.Status.CatalogSync)
	}
	if gw.Status.ToolIndexEndpoint != "" {
		t.Errorf("not-ready endpoint = %q, want empty", gw.Status.ToolIndexEndpoint)
	}

	ready := &kubemootv1alpha1.RAGSource{ObjectMeta: metav1.ObjectMeta{Name: ehToolIndexName}}
	ready.Status.Ready = true
	ready.Status.LastJobName = "index-2"
	ready.Status.QueryEndpoint = "http://gw-tools-query:8000"
	ready.Status.IndexingStats = &kubemootv1alpha1.IndexingStats{LastIndexed: &indexed}
	if !syncToolIndexStatus(ctx, gw, ready) {
		t.Fatal("a ready RAGSource must report ready")
	}
	cs := gw.Status.CatalogSync
	if cs.JobStatus != "Succeeded" || cs.JobName != "index-2" || cs.LastSync != &indexed {
		t.Errorf("ready sync status = %+v", cs)
	}
	if gw.Status.ToolIndexEndpoint != "http://gw-tools-query:8000" {
		t.Errorf("ready endpoint = %q", gw.Status.ToolIndexEndpoint)
	}
}

func TestFindingAgentsBySignal(t *testing.T) {
	events := []transcriptEvent{
		{Type: eventTypeFinding, Agent: "ana", Signal: " Agree "},
		{Type: eventTypeFinding, Agent: "bo", Signal: ehConcern},
		{Type: eventTypeFinding, Agent: "cy", Signal: ehStandAside},
		{Type: eventTypeFinding, Agent: "di", StoodAside: true},
		{Type: eventTypeFinding, Agent: "", Signal: ehAgree},
		{Type: "message", Agent: "ed", Signal: ehAgree},
		{Type: eventTypeFinding, Agent: "fy", Signal: "block"},
	}
	contributing, stoodAside := findingAgentsBySignal(events)
	if len(contributing) != 2 || !contributing["ana"] || !contributing["bo"] {
		t.Errorf("contributing = %v", contributing)
	}
	if len(stoodAside) != 2 || !stoodAside["cy"] || !stoodAside["di"] {
		t.Errorf("stoodAside = %v", stoodAside)
	}
}

func TestQualityAgentTimeout(t *testing.T) {
	if got := qualityAgentTimeout(&kubemootv1alpha1.ConsideringConfig{}); got != 30*time.Second {
		t.Errorf("unset timeout = %v, want 30s", got)
	}
	if got := qualityAgentTimeout(&kubemootv1alpha1.ConsideringConfig{TimeoutSeconds: 7}); got != 7*time.Second {
		t.Errorf("timeout = %v, want 7s", got)
	}
}

func TestReadQualityAgentReply(t *testing.T) {
	reply := func(code int, body string) *http.Response {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
	}
	if got, err := readQualityAgentReply(reply(http.StatusOK, `{"response":"looks safe"}`)); err != nil || got != "looks safe" {
		t.Errorf("ok reply = %q, %v", got, err)
	}
	if _, err := readQualityAgentReply(reply(http.StatusBadGateway, "upstream down")); err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("non-200 reply error = %v, want the body in it", err)
	}
	if _, err := readQualityAgentReply(reply(http.StatusOK, "not json")); err == nil {
		t.Error("expected a parse error")
	}
}

func TestInvalidScheduleMessage(t *testing.T) {
	r := &RAGSourceReconciler{}
	rs := &kubemootv1alpha1.RAGSource{}
	if got := r.invalidScheduleMessage(rs); got != "" {
		t.Errorf("no indexer: got %q", got)
	}
	rs.Spec.Indexer = &kubemootv1alpha1.IndexerConfig{}
	if got := r.invalidScheduleMessage(rs); got != "" {
		t.Errorf("no schedule: got %q", got)
	}
	rs.Spec.Indexer.Schedule = "0 3 * * *"
	if got := r.invalidScheduleMessage(rs); got != "" {
		t.Errorf("valid schedule: got %q", got)
	}
	rs.Spec.Indexer.Schedule = "every tuesday"
	if got := r.invalidScheduleMessage(rs); !strings.HasPrefix(got, "Invalid cron schedule: ") {
		t.Errorf("invalid schedule: got %q", got)
	}
}

func TestQueryServiceDocumentCount(t *testing.T) {
	serve := func(code int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/info" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
		}))
	}
	cases := []struct {
		name   string
		code   int
		body   string
		want   int32
		wantOK bool
	}{
		{"count", http.StatusOK, `{"stats":{"document_count":12}}`, 12, true},
		{"missing count", http.StatusOK, `{"stats":{}}`, 0, true},
		{"non-200", http.StatusServiceUnavailable, `{}`, 0, false},
		{"bad body", http.StatusOK, `not-json-at-all`, 0, false},
	}
	for _, tc := range cases {
		srv := serve(tc.code, tc.body)
		r := &RAGSourceReconciler{HTTPClient: srv.Client()}
		got, ok := r.queryServiceDocumentCount(context.Background(), srv.URL)
		srv.Close()
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}

	unreachable := &RAGSourceReconciler{HTTPClient: &http.Client{Timeout: time.Second}}
	if _, ok := unreachable.queryServiceDocumentCount(context.Background(), "http://127.0.0.1:1"); ok {
		t.Error("an unreachable query service must report false")
	}
}
