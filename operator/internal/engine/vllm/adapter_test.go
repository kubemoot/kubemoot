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

package vllm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	qwen14 = "qwen3:14b"
	qwen8  = "qwen3:8b"

	metricsPage = `# HELP vllm:num_requests_running Number of requests in model execution batches.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{engine="0",model_name="qwen3:14b"} 3.0
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{engine="0",model_name="qwen3:14b"} 2.0
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc{engine="0",model_name="qwen3:14b"} 0.25
`
)

// fakeServer is one vLLM server: its state is set by the test, and every request
// it receives is recorded as "METHOD /path?query".
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	sleeping bool
	// noSleep makes the sleep endpoints answer 404, like a server started without
	// sleep mode.
	noSleep bool
	// sleepStatus, when set, is the status /is_sleeping answers with.
	sleepStatus int
	// unhealthy makes /health answer 503.
	unhealthy bool
	// served is the id /v1/models lists; empty lists the test's model.
	served  string
	metrics string
	log     []string
}

// newFake starts a server serving model; asleep says whether it starts asleep.
func newFake(t *testing.T, model string, asleep bool) *fakeServer {
	t.Helper()
	f := &fakeServer{sleeping: asleep, served: model, metrics: metricsPage}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(r)
	switch r.URL.Path {
	case pathHealth, pathModels, pathIsSleeping, pathMetrics:
		f.read(w, r)
	case pathSleep, pathWakeUp, pathCollective:
		f.mutate(w, r)
	default:
		http.NotFound(w, r)
	}
}

// record appends the request, with its body when it has one, to the log.
func (f *fakeServer) record(r *http.Request) {
	entry := r.Method + " " + r.URL.RequestURI()
	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 {
		entry += " " + string(body)
	}
	f.log = append(f.log, entry)
}

// read handles the GET endpoints.
func (f *fakeServer) read(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == pathHealth && f.unhealthy:
		w.WriteHeader(http.StatusServiceUnavailable)
	case r.URL.Path == pathModels:
		_, _ = fmt.Fprintf(w, `{"data":[{"id":%q}]}`, f.served)
	case r.URL.Path == pathIsSleeping:
		f.readSleeping(w, r)
	case r.URL.Path == pathMetrics && f.metrics != "":
		_, _ = io.WriteString(w, f.metrics)
	case r.URL.Path == pathHealth:
	default:
		http.NotFound(w, r)
	}
}

// readSleeping answers /is_sleeping: 404 without sleep mode, a forced status, or the state.
func (f *fakeServer) readSleeping(w http.ResponseWriter, r *http.Request) {
	switch {
	case f.sleepStatus != 0:
		w.WriteHeader(f.sleepStatus)
	case f.noSleep:
		http.NotFound(w, r)
	default:
		_, _ = fmt.Fprintf(w, `{"is_sleeping":%t}`, f.sleeping)
	}
}

// mutate handles the POST endpoints that change the sleep state.
func (f *fakeServer) mutate(w http.ResponseWriter, r *http.Request) {
	if f.noSleep {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case pathSleep:
		f.sleeping = true
	case pathWakeUp:
		// Level 2 wakes in three calls; only the last (or an untagged one) wakes.
		if tags := r.URL.Query().Get("tags"); tags == "" || tags == "kv_cache" {
			f.sleeping = false
		}
	}
}

// calls is what the server received, in order.
func (f *fakeServer) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log)
}

// posts is the POST requests the server received, in order.
func (f *fakeServer) posts() []string {
	var out []string
	for _, c := range f.calls() {
		if strings.HasPrefix(c, "POST ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeServer) isAsleep() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sleeping
}

// specOf is a vllm provider spec over the given servers, one per model.
func specOf(level int, servers map[string]*fakeServer, order ...string) aiv1alpha1.ModelProviderSpec {
	spec := aiv1alpha1.ModelProviderSpec{
		Type:       aiv1alpha1.ProviderTypeVLLM,
		SleepLevel: level,
	}
	for _, model := range order {
		spec.Servers = append(spec.Servers, aiv1alpha1.VLLMServer{Model: model, Endpoint: servers[model].URL})
	}
	return spec
}

func probe(t *testing.T, spec aiv1alpha1.ModelProviderSpec) engine.Snapshot {
	t.Helper()
	snap, err := New(http.DefaultClient).Probe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return snap
}

func TestProbeAwakeServerReportsStateAndMetrics(t *testing.T) {
	f := newFake(t, qwen14, false)
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))

	if snap.ModelStates[qwen14] != engine.StateAwake {
		t.Errorf("state = %q, want awake", snap.ModelStates[qwen14])
	}
	if !snap.LoadedKnown || len(snap.Loaded) != 1 || snap.Loaded[0].Name != qwen14 {
		t.Errorf("an awake server is the loaded model, got %+v known=%v", snap.Loaded, snap.LoadedKnown)
	}
	if snap.LoadedSizesKnown {
		t.Error("vLLM reports no accelerator memory per model")
	}
	if snap.SwitchCostMs != nil || snap.HostMemoryParkedMiB != nil {
		t.Error("the adapter has not measured switch cost or parked memory")
	}
	wantCounts(t, snap, 3, 2)
	if snap.KVCacheUsage == nil || *snap.KVCacheUsage != 0.25 {
		t.Errorf("kvCacheUsage = %v, want 0.25", snap.KVCacheUsage)
	}
}

// wantCounts fails unless the snapshot reports exactly these request counts.
func wantCounts(t *testing.T, snap engine.Snapshot, running, waiting int) {
	t.Helper()
	if snap.RequestsRunning == nil || snap.RequestsWaiting == nil {
		t.Fatalf("counts missing: %v/%v", snap.RequestsRunning, snap.RequestsWaiting)
	}
	if *snap.RequestsRunning != running || *snap.RequestsWaiting != waiting {
		t.Errorf("requests = %d/%d, want %d/%d", *snap.RequestsRunning, *snap.RequestsWaiting, running, waiting)
	}
}

func TestProbeSleepingServerIsAsleepAndStillAvailable(t *testing.T) {
	f := newFake(t, qwen14, true)
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))

	if snap.ModelStates[qwen14] != engine.StateAsleep {
		t.Errorf("state = %q, want asleep", snap.ModelStates[qwen14])
	}
	if !snap.LoadedKnown || len(snap.Loaded) != 0 {
		t.Errorf("a sleeping server loads nothing, got %+v", snap.Loaded)
	}
	if !snap.AvailableKnown || len(snap.Available) != 1 || snap.Available[0].Name != qwen14 {
		t.Errorf("a sleeping model is still available, got %+v", snap.Available)
	}
}

func TestProbeServerWithoutSleepEndpointsIsAwake(t *testing.T) {
	f := newFake(t, qwen14, false)
	f.noSleep = true
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))
	if snap.ModelStates[qwen14] != engine.StateAwake {
		t.Errorf("a 404 on /is_sleeping counts as awake, got %q", snap.ModelStates[qwen14])
	}
}

func TestProbeAggregatesAcrossServers(t *testing.T) {
	a := newFake(t, qwen14, false)
	b := newFake(t, qwen8, true)
	b.metrics = strings.NewReplacer("3.0", "4.0", "2.0", "1.0", "0.25", "0.75").Replace(metricsPage)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	snap := probe(t, specOf(1, servers, qwen14, qwen8))

	if *snap.RequestsRunning != 7 || *snap.RequestsWaiting != 3 {
		t.Errorf("counts are summed: got %d/%d, want 7/3", *snap.RequestsRunning, *snap.RequestsWaiting)
	}
	if *snap.KVCacheUsage != 0.75 {
		t.Errorf("cache use is the highest: got %v, want 0.75", *snap.KVCacheUsage)
	}
	if snap.ModelStates[qwen14] != engine.StateAwake || snap.ModelStates[qwen8] != engine.StateAsleep {
		t.Errorf("states = %v", snap.ModelStates)
	}
}

func TestProbeKeepsAReportedZeroDistinctFromAbsent(t *testing.T) {
	f := newFake(t, qwen14, false)
	f.metrics = strings.NewReplacer("3.0", "0.0", "2.0", "0.0", "0.25", "0.0").Replace(metricsPage)
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))
	if snap.RequestsRunning == nil || *snap.RequestsRunning != 0 || snap.RequestsWaiting == nil || *snap.RequestsWaiting != 0 {
		t.Errorf("a reported 0 must be kept, got %v/%v", snap.RequestsRunning, snap.RequestsWaiting)
	}
	if snap.KVCacheUsage == nil || *snap.KVCacheUsage != 0 {
		t.Errorf("a reported 0 usage must be kept, got %v", snap.KVCacheUsage)
	}
}

func TestProbeAbsentMetricsStayUnknown(t *testing.T) {
	for name, page := range map[string]string{
		"no /metrics at all":      "",
		"only unrelated families": "# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total 4.2\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, qwen14, false)
			f.metrics = page
			snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))
			if snap.RequestsRunning != nil || snap.RequestsWaiting != nil || snap.KVCacheUsage != nil {
				t.Errorf("absent metrics must stay nil, got %v %v %v", snap.RequestsRunning, snap.RequestsWaiting, snap.KVCacheUsage)
			}
			if snap.ModelStates[qwen14] != engine.StateAwake {
				t.Error("missing metrics must not hide the server")
			}
		})
	}
}

func TestProbeReadsTheOlderCacheMetricName(t *testing.T) {
	f := newFake(t, qwen14, false)
	f.metrics = "# TYPE vllm:gpu_cache_usage_perc gauge\nvllm:gpu_cache_usage_perc{model_name=\"x\"} 0.5\n"
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))
	if snap.KVCacheUsage == nil || *snap.KVCacheUsage != 0.5 {
		t.Errorf("kvCacheUsage = %v, want 0.5 from the older name", snap.KVCacheUsage)
	}
	if snap.RequestsRunning != nil {
		t.Error("a metric the page lacks stays unknown")
	}
}

func TestProbeSumsSamplesOfOneServer(t *testing.T) {
	f := newFake(t, qwen14, false)
	f.metrics = `# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{engine="0"} 2
vllm:num_requests_running{engine="1"} 5
# TYPE vllm:kv_cache_usage_perc gauge
vllm:kv_cache_usage_perc{engine="0"} 0.1
vllm:kv_cache_usage_perc{engine="1"} 0.6
`
	snap := probe(t, specOf(1, map[string]*fakeServer{qwen14: f}, qwen14))
	if *snap.RequestsRunning != 7 || *snap.KVCacheUsage != 0.6 {
		t.Errorf("got %d / %v, want 7 / 0.6", *snap.RequestsRunning, *snap.KVCacheUsage)
	}
}

func TestProbeLeavesOutServersThatDoNotAnswer(t *testing.T) {
	up := newFake(t, qwen14, false)
	down := newFake(t, qwen8, false)
	down.unhealthy = true
	wrong := newFake(t, "other", false)
	servers := map[string]*fakeServer{qwen14: up, qwen8: down, "third": wrong}
	spec := specOf(1, servers, qwen14, qwen8)
	spec.Servers = append(spec.Servers, aiv1alpha1.VLLMServer{Model: "third", Endpoint: wrong.URL})

	snap := probe(t, spec)
	if len(snap.ModelStates) != 1 || snap.ModelStates[qwen14] != engine.StateAwake {
		t.Errorf("only the healthy, correctly named server is reported, got %v", snap.ModelStates)
	}
}

func TestProbeFailsWhenNoServerAnswers(t *testing.T) {
	down := newFake(t, qwen14, false)
	down.unhealthy = true
	if _, err := New(http.DefaultClient).Probe(context.Background(), specOf(1, map[string]*fakeServer{qwen14: down}, qwen14)); err == nil {
		t.Error("a provider with no answering server is an error")
	}
	if _, err := New(http.DefaultClient).Probe(context.Background(), aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeVLLM}); err == nil {
		t.Error("a provider that lists no servers is an error")
	}
}

func TestReadyWakesTheServerAtLevelOne(t *testing.T) {
	a := newFake(t, qwen14, true)
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, map[string]*fakeServer{qwen14: a}, qwen14), qwen14); err != nil {
		t.Fatal(err)
	}
	if got := a.posts(); len(got) != 1 || got[0] != "POST /wake_up" {
		t.Errorf("posts = %v, want one POST /wake_up", got)
	}
}

func TestReadySleepsTheAwakeServerFirst(t *testing.T) {
	a := newFake(t, qwen14, false)
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen8); err != nil {
		t.Fatal(err)
	}
	if got := a.posts(); len(got) != 1 || got[0] != "POST /sleep?level=1" {
		t.Errorf("the awake server is put to sleep, posts = %v", got)
	}
	if got := b.posts(); len(got) != 1 || got[0] != "POST /wake_up" {
		t.Errorf("the target is woken, posts = %v", got)
	}
	if a.isAsleep() != true || b.isAsleep() != false {
		t.Error("end state: the other server asleep, the target awake")
	}
}

func TestReadyDoesNotTouchServersAlreadyInTheRightState(t *testing.T) {
	a := newFake(t, qwen14, false)
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen14); err != nil {
		t.Fatal(err)
	}
	if len(a.posts())+len(b.posts()) != 0 {
		t.Errorf("nothing to do, got posts %v %v", a.posts(), b.posts())
	}
}

func TestReadyLevelTwoWakesInOrder(t *testing.T) {
	a := newFake(t, qwen14, true)
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(2, map[string]*fakeServer{qwen14: a}, qwen14), qwen14); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /wake_up?tags=weights",
		`POST /collective_rpc {"method":"reload_weights"}`,
		"POST /wake_up?tags=kv_cache",
	}
	if got := a.posts(); !slices.Equal(got, want) {
		t.Errorf("posts = %v, want %v", got, want)
	}
}

func TestReadySleepsOthersAtTheProvidersLevel(t *testing.T) {
	a := newFake(t, qwen14, false)
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(2, servers, qwen14, qwen8), qwen8); err != nil {
		t.Fatal(err)
	}
	if got := a.posts(); len(got) != 1 || got[0] != "POST /sleep?level=2" {
		t.Errorf("posts = %v, want sleep at level 2", got)
	}
}

func TestReadySkipsAnUnreachableOtherServer(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.Close()
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen8); err != nil {
		t.Fatalf("a dead server holds no accelerator memory: %v", err)
	}
	if b.isAsleep() {
		t.Error("the target must still wake")
	}
}

func TestReadyFailsWhenAnAwakeServerCannotSleep(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.noSleep = true
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	err := New(http.DefaultClient).Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen8)
	if err == nil || !strings.Contains(err.Error(), "sleep mode") {
		t.Errorf("err = %v, want a sleep-mode error", err)
	}
	if len(b.posts()) != 0 {
		t.Error("the target must not wake while another server still holds the accelerator")
	}
}

func TestReadyAndReleaseRejectAnUnknownModel(t *testing.T) {
	a := newFake(t, qwen14, true)
	spec := specOf(1, map[string]*fakeServer{qwen14: a}, qwen14)
	if err := New(http.DefaultClient).Ready(context.Background(), spec, "nope"); err == nil {
		t.Error("Ready for a model with no server is an error")
	}
	if err := New(http.DefaultClient).Release(context.Background(), spec, "nope"); err == nil {
		t.Error("Release for a model with no server is an error")
	}
}

func TestReleasePutsTheServerToSleepAtTheProvidersLevel(t *testing.T) {
	for _, level := range []int{0, 1, 2} {
		a := newFake(t, qwen14, false)
		spec := specOf(level, map[string]*fakeServer{qwen14: a}, qwen14)
		if err := New(http.DefaultClient).Release(context.Background(), spec, qwen14); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("POST /sleep?level=%d", max(level, 1))
		if got := a.posts(); len(got) != 1 || got[0] != want {
			t.Errorf("level %d: posts = %v, want %s", level, got, want)
		}
	}
}

func TestReleaseLeavesASleepingServerAlone(t *testing.T) {
	a := newFake(t, qwen14, true)
	if err := New(http.DefaultClient).Release(context.Background(), specOf(1, map[string]*fakeServer{qwen14: a}, qwen14), qwen14); err != nil {
		t.Fatal(err)
	}
	if len(a.posts()) != 0 {
		t.Errorf("posts = %v, want none", a.posts())
	}
}

func TestReleaseFailsWithoutSleepMode(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.noSleep = true
	if err := New(http.DefaultClient).Release(context.Background(), specOf(1, map[string]*fakeServer{qwen14: a}, qwen14), qwen14); err == nil {
		t.Error("a server without sleep endpoints cannot be released")
	}
}

func TestReadyOnAServerWithoutSleepEndpointsNeedsNothing(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.noSleep = true
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, map[string]*fakeServer{qwen14: a}, qwen14), qwen14); err != nil {
		t.Fatalf("a server that is always awake is already ready: %v", err)
	}
	if len(a.posts()) != 0 {
		t.Errorf("posts = %v, want none", a.posts())
	}
}

func TestReadyFailsWhenAnotherServersSleepStateCannotBeRead(t *testing.T) {
	a := newFake(t, qwen14, false)
	a.sleepStatus = http.StatusInternalServerError
	b := newFake(t, qwen8, true)
	servers := map[string]*fakeServer{qwen14: a, qwen8: b}
	if err := New(http.DefaultClient).Ready(context.Background(), specOf(1, servers, qwen14, qwen8), qwen8); err == nil {
		t.Error("a server that answers /health but not /is_sleeping may still hold the accelerator")
	}
	if len(b.posts()) != 0 {
		t.Error("the target must not wake")
	}
}

func TestEndpointWithTrailingSlash(t *testing.T) {
	a := newFake(t, qwen14, true)
	spec := specOf(1, map[string]*fakeServer{qwen14: a}, qwen14)
	spec.Servers[0].Endpoint += "/"
	if err := New(http.DefaultClient).Ready(context.Background(), spec, qwen14); err != nil {
		t.Fatal(err)
	}
	for _, c := range a.calls() {
		if strings.Contains(c, "//") {
			t.Errorf("request %q has a doubled slash", c)
		}
	}
}
