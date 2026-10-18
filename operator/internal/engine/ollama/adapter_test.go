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

package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	psBody   = `{"models":[{"name":"qwen3:14b","size_vram":2147483648,"size":3000,"context_length":8192}]}`
	tagsBody = `{"models":[{"name":"qwen3:14b","size":9000},{"name":"llama3:8b","size":4096}]}`
	testPS   = "/api/ps"
	testTags = "/api/tags"
	testGen  = "/api/generate"
	jsonType = "application/json"
	modelTag = "qwen3:14b"
)

// fakeOllama answers /api/ps and /api/tags with the given bodies (a status other
// than 200 when the body is empty) and records what /api/generate receives.
func fakeOllama(t *testing.T, ps, tags string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var generated []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPS:
			answer(w, ps)
		case testTags:
			answer(w, tags)
		case testGen:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			generated = append(generated, body)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &generated
}

func answer(w http.ResponseWriter, body string) {
	if body == "" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", jsonType)
	_, _ = w.Write([]byte(body))
}

func TestProbeReportsLoadedAndAvailableStates(t *testing.T) {
	srv, _ := fakeOllama(t, psBody, tagsBody)
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ModelStates[modelTag] != engine.StateAwake {
		t.Errorf("%s = %q, want awake", modelTag, snap.ModelStates[modelTag])
	}
	if snap.ModelStates["llama3:8b"] != engine.StateAvailable {
		t.Errorf("llama3:8b = %q, want available", snap.ModelStates["llama3:8b"])
	}
	if len(snap.ModelStates) != 2 {
		t.Errorf("ModelStates = %v, want 2 entries", snap.ModelStates)
	}
}

func TestProbeReportsLoadedAndAvailableModels(t *testing.T) {
	srv, _ := fakeOllama(t, psBody, tagsBody)
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.LoadedKnown || len(snap.Loaded) != 1 || snap.Loaded[0].ContextLength != 8192 {
		t.Errorf("Loaded = %+v", snap.Loaded)
	}
	if !snap.AvailableKnown || len(snap.Available) != 2 {
		t.Errorf("Available = %+v", snap.Available)
	}
	if snap.VRAMUsedMiB() != 2048 {
		t.Errorf("VRAMUsedMiB = %d, want 2048", snap.VRAMUsedMiB())
	}
}

func TestProbeLeavesEngineMetricsAbsent(t *testing.T) {
	srv, _ := fakeOllama(t, psBody, tagsBody)
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if snap.RequestsRunning != nil || snap.RequestsWaiting != nil || snap.KVCacheUsage != nil ||
		snap.SwitchCostMs != nil || snap.HostMemoryParkedMiB != nil {
		t.Errorf("Ollama exports none of these, got %+v", snap)
	}
}

func TestProbeNothingLoadedIsKnownEmpty(t *testing.T) {
	srv, _ := fakeOllama(t, `{"models":[]}`, tagsBody)
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.LoadedKnown || len(snap.Loaded) != 0 {
		t.Errorf("an empty /api/ps is known and empty, got %+v known=%v", snap.Loaded, snap.LoadedKnown)
	}
	if snap.ModelStates[modelTag] != engine.StateAvailable {
		t.Errorf("%s = %q, want available", modelTag, snap.ModelStates[modelTag])
	}
}

func TestProbeWithoutPSLeavesStatesUnknown(t *testing.T) {
	srv, _ := fakeOllama(t, "", tagsBody)
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LoadedKnown || snap.ModelStates != nil {
		t.Errorf("without /api/ps nothing about loading is known, got %+v", snap)
	}
	if !snap.AvailableKnown || len(snap.Available) != 2 {
		t.Errorf("Available = %+v", snap.Available)
	}
}

func TestProbeWithoutTagsStillReportsAwake(t *testing.T) {
	srv, _ := fakeOllama(t, psBody, "")
	snap, err := New(srv.Client()).Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if snap.AvailableKnown || snap.ModelStates[modelTag] != engine.StateAwake {
		t.Errorf("got %+v", snap)
	}
}

func TestProbeProviderThatDoesNotAnswer(t *testing.T) {
	srv, _ := fakeOllama(t, psBody, tagsBody)
	srv.Close()
	if _, err := New(srv.Client()).Probe(context.Background(), srv.URL); err == nil {
		t.Fatal("a provider that does not answer must fail the probe")
	}
}

func TestProbeRefusedBothEndpoints(t *testing.T) {
	srv, _ := fakeOllama(t, "", "")
	if _, err := New(srv.Client()).Probe(context.Background(), srv.URL); err == nil {
		t.Fatal("both endpoints failing must fail the probe")
	}
}

func TestProbeUnreadableAnswer(t *testing.T) {
	srv, _ := fakeOllama(t, "not json", "not json")
	if _, err := New(srv.Client()).Probe(context.Background(), srv.URL); err == nil {
		t.Fatal("answers that do not decode must fail the probe")
	}
}

func TestReadyLoadsWithoutKeepAliveOverride(t *testing.T) {
	srv, generated := fakeOllama(t, psBody, tagsBody)
	if err := New(srv.Client()).Ready(context.Background(), srv.URL, modelTag); err != nil {
		t.Fatal(err)
	}
	if len(*generated) != 1 || (*generated)[0]["model"] != modelTag {
		t.Fatalf("generate calls = %v", *generated)
	}
	if _, set := (*generated)[0]["keep_alive"]; set {
		t.Errorf("ready must not set keep_alive: %v", (*generated)[0])
	}
}

func TestReleaseUnloadsWithKeepAliveZero(t *testing.T) {
	srv, generated := fakeOllama(t, psBody, tagsBody)
	if err := New(srv.Client()).Release(context.Background(), srv.URL, modelTag); err != nil {
		t.Fatal(err)
	}
	if len(*generated) != 1 || (*generated)[0]["keep_alive"] != float64(0) || (*generated)[0]["model"] != modelTag {
		t.Fatalf("generate calls = %v", *generated)
	}
}

func TestReadyAndReleaseReportFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	a := New(failing.Client())
	if err := a.Ready(context.Background(), failing.URL, modelTag); err == nil {
		t.Error("Ready must fail on a 500")
	}
	if err := a.Release(context.Background(), failing.URL, modelTag); err == nil {
		t.Error("Release must fail on a 500")
	}
	failing.Close()
	if err := a.Ready(context.Background(), failing.URL, modelTag); err == nil {
		t.Error("Ready must fail when the provider does not answer")
	}
}
