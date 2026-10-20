/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package probe

import (
	"context"
	"testing"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/storetest"
)

func vllmRequest(kind string) Request {
	r := request(kind)
	r.EngineType = "vllm"
	return r
}

func TestAVLLMProbeUsesItsOwnEngine_andFinishReleasesThroughTheEngineThatWokeIt(t *testing.T) {
	kv, ollama, vllm := storetest.NewMemKV(), storetest.NewEngine(), storetest.NewEngine()
	p := newProber(kv, ollama, &clock{t0})
	var asked []Request
	p.For = func(req Request) engine.Engine {
		asked = append(asked, req)
		return vllm
	}

	r, err := p.Probe(context.Background(), vllmRequest(KindLoad)) // the model was asleep: a cold probe
	check(t, "no error", err == nil && r.Fits, err)
	check(t, "the engine chosen by the request served it", len(asked) == 1 && vllm.Generates == 1 && ollama.Generates == 0,
		asked)
	check(t, "remembered for Finish", len(p.loaded[server]) == 1, p.loaded)

	unloaded, err := p.Finish(context.Background(), server)
	check(t, "finish", err == nil && len(unloaded) == 1, err)
	check(t, "the server is put back through the engine that woke it, not the Ollama client",
		len(vllm.Unloads) == 1 && len(ollama.Unloads) == 0, vllm.Unloads)
}

func TestAnAlreadyAwakeVLLMModelIsNotOursToPutToSleep(t *testing.T) {
	kv, ollama, vllm := storetest.NewMemKV(), storetest.NewEngine(), storetest.NewEngine()
	vllm.Resident[model] = engine.Resident{Model: model}
	p := newProber(kv, ollama, &clock{t0})
	p.For = func(Request) engine.Engine { return vllm }

	_, err := p.Probe(context.Background(), vllmRequest(KindShort))
	check(t, "no error", err == nil, err)
	check(t, "not remembered, so Finish leaves it awake", len(p.loaded) == 0, p.loaded)
}

func TestTheOllamaEngineIsUsedWhenThereIsNoForOrNoEngineType(t *testing.T) {
	kv, ollama, other := storetest.NewMemKV(), storetest.NewEngine(), storetest.NewEngine()
	p := newProber(kv, ollama, &clock{t0})
	p.For = func(Request) engine.Engine { return other }

	_, err := p.Probe(context.Background(), request(KindShort)) // no EngineType
	check(t, "no error", err == nil, err)
	check(t, "an Ollama request ignores For", ollama.Generates == 1 && other.Generates == 0, ollama.Generates)

	p.For = nil
	_, err = p.Probe(context.Background(), vllmRequest(KindShort)) // no For to ask
	check(t, "no error without For", err == nil && ollama.Generates == 2, err)
}
