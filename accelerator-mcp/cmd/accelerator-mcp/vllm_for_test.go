package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/probe"
)

type noLifecycle struct{}

func (noLifecycle) Ready(context.Context, string, string) error   { return nil }
func (noLifecycle) Release(context.Context, string, string) error { return nil }

func TestVllmForBuildsTheClientOfTheProbedModelsServerWithTheLifecycle(t *testing.T) {
	lifecycle := noLifecycle{}
	client := &http.Client{}
	got := vllmFor(client, lifecycle)(probe.Request{Server: "vllm-rig1", Endpoint: "http://v:8000", Model: "qwen3:14b"})
	e, ok := got.(engine.VLLMEngine)
	if !ok {
		t.Fatalf("a vLLM probe needs the vLLM engine, got %T", got)
	}
	if e.Endpoint != "http://v:8000" || e.Server != "vllm-rig1" || e.Model != "qwen3:14b" || e.Lifecycle != lifecycle ||
		e.Client != client {
		t.Errorf("engine = %+v", e)
	}
}
