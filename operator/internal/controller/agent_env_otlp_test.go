package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestOTLPEnvIsAbsentWithoutACollector(t *testing.T) {
	if got := (&AgentReconciler{}).otlpEnvVars(); len(got) != 0 {
		t.Fatalf("no cache: got %v", got)
	}
	r := &AgentReconciler{ConfigCache: NewConfigCache()}
	if got := r.otlpEnvVars(); len(got) != 0 {
		t.Fatalf("no config: got %v", got)
	}
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{})
	if got := r.otlpEnvVars(); len(got) != 0 {
		t.Fatalf("config without a collector: got %v", got)
	}
}

func TestOTLPEnvCarriesTheConfiguredCollector(t *testing.T) {
	r := &AgentReconciler{ConfigCache: NewConfigCache()}
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Defaults: kubemootv1alpha1.DefaultConfig{OTelCollectorEndpoint: "http://otel-collector.observability:4318"},
	}})
	got := r.otlpEnvVars()
	if len(got) != 1 || got[0].Name != "KUBEMOOT_OTLP_ENDPOINT" || got[0].Value != "http://otel-collector.observability:4318" {
		t.Fatalf("got %v", got)
	}
}
