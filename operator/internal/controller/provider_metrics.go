/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"slices"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The label names of the provider metrics.
const (
	labelProvider = "provider"
	labelModel    = "model"
)

// The operator's metrics about each ModelProvider: its calibration and the speeds
// the scheduler plans with. They are served on the operator's metrics endpoint with
// controller-runtime's own metrics.
var (
	providerCalibrationPhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubemoot_provider_calibration_phase",
		Help: "1 for each ModelProvider's current calibration phase (Calibrating, Calibrated, Waiting, Failed, or None)",
	}, []string{labelProvider, "phase"})
	providerModelTokensPerSecond = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubemoot_provider_model_tokens_per_second",
		Help: "Speed of each model on each ModelProvider, by kind (generate, prompt) and source (calibrated, " +
			"or observed: the median of warm crew calls that ran alone since the calibration)",
	}, []string{labelProvider, labelModel, "kind", "source"})
	providerModelLoadSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubemoot_provider_model_load_seconds",
		Help: "How long each model took to load on each ModelProvider when it was calibrated",
	}, []string{labelProvider, labelModel})
	providerModelDrifted = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubemoot_provider_model_drifted",
		Help: "1 for each model whose crew calls have drifted from its calibration on a ModelProvider",
	}, []string{labelProvider, labelModel})
)

func init() {
	metrics.Registry.MustRegister(providerCalibrationPhase, providerModelTokensPerSecond,
		providerModelLoadSeconds, providerModelDrifted)
}

// providerSeries is the series one recording sets, so the next recording can remove
// the ones it no longer sets without a moment where the provider has none.
type providerSeries map[*prometheus.GaugeVec][][]string

func (ps providerSeries) set(vec *prometheus.GaugeVec, value float64, labels ...string) {
	vec.WithLabelValues(labels...).Set(value)
	ps[vec] = append(ps[vec], labels)
}

// removeStale deletes the series in old that ps does not set again.
func (ps providerSeries) removeStale(old providerSeries) {
	for vec, sets := range old {
		for _, labels := range sets {
			if !slices.ContainsFunc(ps[vec], func(l []string) bool { return slices.Equal(l, labels) }) {
				vec.DeleteLabelValues(labels...)
			}
		}
	}
}

var (
	seriesMu sync.Mutex
	// seriesOf is what the last recording set for each provider.
	seriesOf = map[string]providerSeries{}
)

// recordProviderMetrics sets the provider's metrics from its status and speeds, and
// removes the series a model or phase that went away left behind.
func recordProviderMetrics(p *aiv1alpha1.ModelProvider, speeds map[string]ModelSpeed) {
	ps := providerSeries{}
	phase := "None"
	if cal := p.Status.Calibration; cal != nil && cal.Phase != "" {
		phase = cal.Phase
	}
	ps.set(providerCalibrationPhase, 1, p.Name, phase)
	for model, s := range speeds {
		ps.set(providerModelTokensPerSecond, float64(s.GenerateTokensPerSecond), p.Name, model, "generate", "calibrated")
		if s.PromptTokensPerSecond > 0 {
			ps.set(providerModelTokensPerSecond, float64(s.PromptTokensPerSecond), p.Name, model, "prompt", "calibrated")
		}
		if s.ObservedGenerateTokensPerSecond > 0 {
			ps.set(providerModelTokensPerSecond, float64(s.ObservedGenerateTokensPerSecond), p.Name, model, "generate", "observed")
		}
		if s.LoadMs > 0 {
			ps.set(providerModelLoadSeconds, float64(s.LoadMs)/1000, p.Name, model)
		}
	}
	if d := p.Status.CalibrationDrift; d != nil {
		for _, m := range d.Models {
			ps.set(providerModelDrifted, 1, p.Name, m.Model)
		}
	}
	seriesMu.Lock()
	defer seriesMu.Unlock()
	ps.removeStale(seriesOf[p.Name])
	seriesOf[p.Name] = ps
}

// forgetProviderMetrics removes every series about the provider.
func forgetProviderMetrics(provider string) {
	seriesMu.Lock()
	delete(seriesOf, provider)
	seriesMu.Unlock()
	match := prometheus.Labels{labelProvider: provider}
	providerCalibrationPhase.DeletePartialMatch(match)
	providerModelTokensPerSecond.DeletePartialMatch(match)
	providerModelLoadSeconds.DeletePartialMatch(match)
	providerModelDrifted.DeletePartialMatch(match)
}
