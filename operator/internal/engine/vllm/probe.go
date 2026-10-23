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
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

// The metrics vLLM exports that the contract carries. The cache gauge was
// renamed from gpu_cache_usage_perc to kv_cache_usage_perc; the current name is
// tried first and the older one is read from servers that still export it.
const (
	metricRunning = "vllm:num_requests_running"
	metricWaiting = "vllm:num_requests_waiting"
)

var kvCacheMetrics = []string{"vllm:kv_cache_usage_perc", "vllm:gpu_cache_usage_perc"}

// serverProbe is what one server answered. A nil field was not read.
type serverProbe struct {
	// models are the served names the server answers to (one, or more when it has aliases).
	models []string
	// contexts is the context window of each served name /v1/models reported one for.
	contexts map[string]int
	// state is empty when the server answered but its sleep state could not be read.
	state   engine.ModelState
	running *float64
	waiting *float64
	kvUsage *float64
}

// Probe reads every vLLM server of the ModelServer. A server that does not answer, or
// answers for a different model than the spec names, is left out of the
// snapshot; Probe fails only when no server answered. Request counts are summed
// over the servers that reported them and cache use is the highest reported, so
// the snapshot describes the ModelServer, not one vLLM server. The adapter reports no
// switch cost and no parked host memory: it has not measured either.
func (a *Adapter) Probe(ctx context.Context, spec aiv1alpha1.ModelServerSpec) (engine.Snapshot, error) {
	servers := serversOf(spec)
	if len(servers) == 0 {
		return engine.Snapshot{}, errors.New("the vllm server lists no servers")
	}
	var probes []serverProbe
	var errs []error
	for _, g := range byEndpoint(servers) {
		p, err := a.probeServer(ctx, g)
		if err != nil {
			errs = append(errs, fmt.Errorf("server for %q: %w", g.models[0], err))
			continue
		}
		probes = append(probes, p)
	}
	if len(probes) == 0 {
		return engine.Snapshot{}, errors.Join(errs...)
	}
	return snapshotOf(probes), nil
}

// endpointModels is one server and every served name it answers to.
type endpointModels struct {
	endpoint string
	models   []string
}

// byEndpoint groups the listed servers by endpoint, in first-seen order. A server
// with several served names (aliases) is listed once per name, and is probed once so
// its request counts are not added up once per name.
func byEndpoint(servers []aiv1alpha1.VLLMServer) []endpointModels {
	var groups []endpointModels
	index := map[string]int{}
	for _, srv := range servers {
		i, ok := index[srv.Endpoint]
		if !ok {
			i = len(groups)
			index[srv.Endpoint] = i
			groups = append(groups, endpointModels{endpoint: srv.Endpoint})
		}
		groups[i].models = append(groups[i].models, srv.Model)
	}
	return groups
}

// probeServer reads one server. It fails when /health does not answer 200, or
// when /v1/models answers and does not list every model the spec says the server
// serves; sleep state and metrics are best effort.
func (a *Adapter) probeServer(ctx context.Context, g endpointModels) (serverProbe, error) {
	p := serverProbe{models: g.models}
	if _, err := a.get(ctx, g.endpoint, pathHealth); err != nil {
		return p, err
	}
	served, err := a.ServedModels(ctx, g.endpoint)
	if err == nil {
		if err := checkServes(served, g); err != nil {
			return p, err
		}
		p.contexts = contextsOf(served, g.models)
	}
	p.state = a.sleepState(ctx, g.endpoint)
	a.readMetrics(ctx, g.endpoint, &p)
	return p, nil
}

// checkServes verifies the served names against a readable /v1/models list; a
// list that did not answer is not proof of a mismatch, so only a readable list
// without a name fails.
func checkServes(served []ServedModel, g endpointModels) error {
	ids := make([]string, len(served))
	for i, m := range served {
		ids[i] = m.ID
	}
	for _, want := range g.models {
		if !slices.Contains(ids, want) {
			return fmt.Errorf("serves %v, not %q: check --served-model-name", ids, want)
		}
	}
	return nil
}

// contextsOf is the window /v1/models reports for each of the wanted names; a name
// whose entry carries no max_model_len is left out.
func contextsOf(served []ServedModel, wanted []string) map[string]int {
	var out map[string]int
	for _, m := range served {
		if m.MaxModelLen <= 0 || !slices.Contains(wanted, m.ID) {
			continue
		}
		if out == nil {
			out = map[string]int{}
		}
		out[m.ID] = m.MaxModelLen
	}
	return out
}

// sleepState is awake or asleep from /is_sleeping. A server without the sleep
// endpoints (404) is awake. Any other failure leaves the state unknown.
func (a *Adapter) sleepState(ctx context.Context, endpoint string) engine.ModelState {
	sleeping, err := a.isSleeping(ctx, endpoint)
	switch {
	case isNotFound(err):
		return engine.StateAwake
	case err != nil:
		return ""
	case sleeping:
		return engine.StateAsleep
	}
	return engine.StateAwake
}

// readMetrics fills p's request counts and cache use from /metrics; whatever the
// page does not carry stays nil.
func (a *Adapter) readMetrics(ctx context.Context, endpoint string, p *serverProbe) {
	body, err := a.get(ctx, endpoint, pathMetrics)
	if err != nil {
		return
	}
	families, err := parseMetrics(body)
	if err != nil {
		return
	}
	p.running = sumOf(families, metricRunning)
	p.waiting = sumOf(families, metricWaiting)
	for _, name := range kvCacheMetrics {
		if p.kvUsage = maxOf(families, name); p.kvUsage != nil {
			return
		}
	}
}

// parseMetrics parses a Prometheus text page.
func parseMetrics(body []byte) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(bytes.NewReader(body))
}

// sumOf adds the gauge samples of the named family; nil when it is absent or empty.
func sumOf(families map[string]*dto.MetricFamily, name string) *float64 {
	return sumOrNil(gaugeValues(families, name))
}

// maxOf is the highest gauge sample of the named family; nil when it is absent or empty.
func maxOf(families map[string]*dto.MetricFamily, name string) *float64 {
	return maxOrNil(gaugeValues(families, name))
}

// sumOrNil adds values; nil when there are none, so a read 0 stays distinct from absent.
func sumOrNil(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var total float64
	for _, v := range values {
		total += v
	}
	return &total
}

// maxOrNil is the largest value; nil when there are none.
func maxOrNil(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	highest := slices.Max(values)
	return &highest
}

// wholeCount rounds a summed count to an int; nil stays nil.
func wholeCount(v *float64) *int {
	if v == nil {
		return nil
	}
	n := int(*v + 0.5)
	return &n
}

// gaugeValues is every sample of a gauge family (a family has one per engine
// and model label set).
func gaugeValues(families map[string]*dto.MetricFamily, name string) []float64 {
	fam, ok := families[name]
	if !ok {
		return nil
	}
	var out []float64
	for _, m := range fam.GetMetric() {
		if v, ok := sampleValue(m); ok {
			out = append(out, v)
		}
	}
	return out
}

// sampleValue is a sample's value whether the page typed the family a gauge or
// left it untyped.
func sampleValue(m *dto.Metric) (float64, bool) {
	if g := m.GetGauge(); g != nil {
		return g.GetValue(), true
	}
	if u := m.GetUntyped(); u != nil {
		return u.GetValue(), true
	}
	return 0, false
}

// snapshotOf assembles the server snapshot from the servers that answered.
func snapshotOf(probes []serverProbe) engine.Snapshot {
	snap := engine.Snapshot{
		ModelStates:    map[string]engine.ModelState{},
		LoadedKnown:    true,
		AvailableKnown: true,
	}
	var running, waiting, usage []float64
	for _, p := range probes {
		addModels(&snap, p)
		addContexts(&snap, p)
		running = appendIf(running, p.running)
		waiting = appendIf(waiting, p.waiting)
		usage = appendIf(usage, p.kvUsage)
	}
	snap.RequestsRunning = wholeCount(sumOrNil(running))
	snap.RequestsWaiting = wholeCount(sumOrNil(waiting))
	snap.KVCacheUsage = maxOrNil(usage)
	return snap
}

// addModels records each served name of a probed server as available, with the
// server's state when it could be read.
func addModels(snap *engine.Snapshot, p serverProbe) {
	if p.state == "" {
		snap.LoadedKnown = false
	}
	for _, name := range p.models {
		snap.Available = append(snap.Available, aiv1alpha1.AvailableModel{Name: name})
		if p.state == "" {
			continue
		}
		snap.ModelStates[name] = p.state
		if p.state == engine.StateAwake {
			snap.Loaded = append(snap.Loaded, aiv1alpha1.LoadedModel{Name: name})
		}
	}
}

// addContexts records the window each of a probed server's models runs with.
func addContexts(snap *engine.Snapshot, p serverProbe) {
	for name, window := range p.contexts {
		if snap.ContextLengths == nil {
			snap.ContextLengths = map[string]int{}
		}
		snap.ContextLengths[name] = window
	}
}

// appendIf adds v to values when it was read.
func appendIf(values []float64, v *float64) []float64 {
	if v == nil {
		return values
	}
	return append(values, *v)
}
