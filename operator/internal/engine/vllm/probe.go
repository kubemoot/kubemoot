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
	model string
	// state is empty when the server answered but its sleep state could not be read.
	state   engine.ModelState
	running *float64
	waiting *float64
	kvUsage *float64
}

// Probe reads every server of the provider. A server that does not answer, or
// answers for a different model than the spec names, is left out of the
// snapshot; Probe fails only when no server answered. Request counts are summed
// over the servers that reported them and cache use is the highest reported, so
// the snapshot describes the provider, not one server. The adapter reports no
// switch cost and no parked host memory: it has not measured either.
func (a *Adapter) Probe(ctx context.Context, spec aiv1alpha1.ModelProviderSpec) (engine.Snapshot, error) {
	servers := serversOf(spec)
	if len(servers) == 0 {
		return engine.Snapshot{}, errors.New("the vllm provider lists no servers")
	}
	var probes []serverProbe
	var errs []error
	for _, srv := range servers {
		p, err := a.probeServer(ctx, srv)
		if err != nil {
			errs = append(errs, fmt.Errorf("server for %q: %w", srv.Model, err))
			continue
		}
		probes = append(probes, p)
	}
	if len(probes) == 0 {
		return engine.Snapshot{}, errors.Join(errs...)
	}
	return snapshotOf(probes), nil
}

// probeServer reads one server. It fails when /health does not answer 200, or
// when /v1/models answers and does not list the model the spec says the server
// serves; sleep state and metrics are best effort.
func (a *Adapter) probeServer(ctx context.Context, srv aiv1alpha1.VLLMServer) (serverProbe, error) {
	p := serverProbe{model: srv.Model}
	if _, err := a.get(ctx, srv.Endpoint, pathHealth); err != nil {
		return p, err
	}
	if err := a.checkServes(ctx, srv); err != nil {
		return p, err
	}
	p.state = a.sleepState(ctx, srv.Endpoint)
	a.readMetrics(ctx, srv.Endpoint, &p)
	return p, nil
}

// checkServes verifies the served name. A /v1/models that does not answer is not
// proof of a mismatch, so only a readable list without the name fails.
func (a *Adapter) checkServes(ctx context.Context, srv aiv1alpha1.VLLMServer) error {
	body, err := a.get(ctx, srv.Endpoint, pathModels)
	if err != nil {
		return nil
	}
	names, err := servedNames(body)
	if err != nil || slices.Contains(names, srv.Model) {
		return nil
	}
	return fmt.Errorf("serves %v, not %q: check --served-model-name", names, srv.Model)
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

// snapshotOf assembles the provider snapshot from the servers that answered.
func snapshotOf(probes []serverProbe) engine.Snapshot {
	snap := engine.Snapshot{
		ModelStates:    map[string]engine.ModelState{},
		LoadedKnown:    true,
		AvailableKnown: true,
	}
	var running, waiting, usage []float64
	for _, p := range probes {
		snap.Available = append(snap.Available, aiv1alpha1.AvailableModel{Name: p.model})
		switch p.state {
		case "":
			snap.LoadedKnown = false
		case engine.StateAwake:
			snap.Loaded = append(snap.Loaded, aiv1alpha1.LoadedModel{Name: p.model})
			snap.ModelStates[p.model] = p.state
		default:
			snap.ModelStates[p.model] = p.state
		}
		running = appendIf(running, p.running)
		waiting = appendIf(waiting, p.waiting)
		usage = appendIf(usage, p.kvUsage)
	}
	snap.RequestsRunning = wholeCount(sumOrNil(running))
	snap.RequestsWaiting = wholeCount(sumOrNil(waiting))
	snap.KVCacheUsage = maxOrNil(usage)
	return snap
}

// appendIf adds v to values when it was read.
func appendIf(values []float64, v *float64) []float64 {
	if v == nil {
		return values
	}
	return append(values, *v)
}
