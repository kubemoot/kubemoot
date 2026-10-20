/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// testServer is the server the metric tests record.
const testServer = "metrics-test-gpu"

func TestRecordServerMetrics(t *testing.T) {
	const server = testServer
	defer forgetServerMetrics(server)
	p := &aiv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: server}}
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: "Calibrated"}
	p.Status.CalibrationDrift = &aiv1alpha1.CalibrationDrift{Models: []aiv1alpha1.ModelDrift{{Model: "a:8b"}}}

	recordServerMetrics(p, map[string]ModelSpeed{
		"a:8b":  {LoadMs: 2_900, PromptTokensPerSecond: 15_000, GenerateTokensPerSecond: 235, ObservedGenerateTokensPerSecond: 60},
		"b:14b": {GenerateTokensPerSecond: 130},
	})
	if v := testutil.ToFloat64(serverCalibrationPhase.WithLabelValues(server, "Calibrated")); v != 1 {
		t.Errorf("phase: %v", v)
	}
	if v := testutil.ToFloat64(serverModelTokensPerSecond.WithLabelValues(server, "a:8b", "generate", "observed")); v != 60 {
		t.Errorf("observed speed: %v", v)
	}
	if v := testutil.ToFloat64(serverModelLoadSeconds.WithLabelValues(server, "a:8b")); v != 2.9 {
		t.Errorf("load seconds: %v", v)
	}
	if v := testutil.ToFloat64(serverModelDrifted.WithLabelValues(server, "a:8b")); v != 1 {
		t.Errorf("drifted: %v", v)
	}
	// b:14b has no prompt speed, load time, or observed speed: those series are absent.
	if n := seriesOfServer(serverModelTokensPerSecond); n != 4 {
		t.Errorf("speed series: %d, want 4 (a: generate calibrated and observed, prompt; b: generate)", n)
	}

	// A new phase and fewer models replace the old series.
	p.Status.Calibration.Phase = "Waiting"
	p.Status.CalibrationDrift = nil
	recordServerMetrics(p, map[string]ModelSpeed{"b:14b": {GenerateTokensPerSecond: 130}})
	if n := seriesOfServer(serverCalibrationPhase); n != 1 {
		t.Errorf("phase series after a change: %d", n)
	}
	if n := seriesOfServer(serverModelDrifted); n != 0 {
		t.Errorf("drift series after the drift cleared: %d", n)
	}

	forgetServerMetrics(server)
	if n := seriesOfServer(serverModelTokensPerSecond); n != 0 {
		t.Errorf("series left after forgetting the server: %d", n)
	}
	uncalibrated := &aiv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: server}}
	recordServerMetrics(uncalibrated, nil)
	if v := testutil.ToFloat64(serverCalibrationPhase.WithLabelValues(server, "None")); v != 1 {
		t.Errorf("an uncalibrated server's phase: %v", v)
	}
}

// Every server metric has a description, which Prometheus serves as its HELP text.
func TestServerMetricsHaveDescriptions(t *testing.T) {
	for _, c := range []prometheus.Collector{serverCalibrationPhase, serverModelTokensPerSecond,
		serverModelLoadSeconds, serverModelDrifted} {
		ch := make(chan *prometheus.Desc, 1)
		c.Describe(ch)
		d := (<-ch).String()
		if strings.Contains(d, `help: ""`) || !strings.Contains(d, "kubemoot_server_") {
			t.Errorf("metric without a description: %s", d)
		}
	}
}

// seriesOfServer counts the vector's series about testServer; other tests in
// the package record other servers on the same registry.
func seriesOfServer(vec *prometheus.GaugeVec) int {
	ch := make(chan prometheus.Metric, 64)
	go func() { vec.Collect(ch); close(ch) }()
	n := 0
	for m := range ch {
		var out dto.Metric
		if m.Write(&out) != nil {
			continue
		}
		for _, l := range out.GetLabel() {
			if l.GetName() == labelServer && l.GetValue() == testServer {
				n++
			}
		}
	}
	return n
}
