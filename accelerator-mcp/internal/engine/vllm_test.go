/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const vllmModel = "qwen3:14b"

// fakeServer is a vLLM server: it streams a completion after a prefill delay and a
// per-token delay, and records every path hit, so a test can see that nothing woke it
// directly.
type fakeServer struct {
	mu       sync.Mutex
	asleep   bool
	hits     []string
	prefill  time.Duration
	perToken time.Duration
	tokens   int
	noTokens bool
}

func (f *fakeServer) setAsleep(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asleep = v
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits = append(f.hits, r.Method+" "+r.URL.Path)
		asleep := f.asleep
		f.mu.Unlock()
		switch r.URL.Path {
		case "/is_sleeping":
			_, _ = fmt.Fprintf(w, `{"is_sleeping":%t}`, asleep)
		case "/v1/models":
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q,"max_model_len":32768}]}`, vllmModel)
		case "/v1/chat/completions":
			f.stream(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeServer) stream(w http.ResponseWriter) {
	flusher := w.(http.Flusher)
	time.Sleep(f.prefill)
	if !f.noTokens {
		for i := 0; i < f.tokens; i++ {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			flusher.Flush()
			time.Sleep(f.perToken)
		}
	}
	_, _ = fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1500,\"completion_tokens\":%d}}\n\n", f.tokens)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (f *fakeServer) wasHit(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hits {
		if strings.HasSuffix(h, " "+path) {
			return true
		}
	}
	return false
}

// fakeLifecycle stands in for the operator: a wake flips the fake server awake.
type fakeLifecycle struct {
	server  *fakeServer
	mu      sync.Mutex
	readied []string
	freed   []string
	err     error
	slow    time.Duration
}

func (l *fakeLifecycle) Ready(_ context.Context, server, model string) error {
	l.mu.Lock()
	l.readied = append(l.readied, server+"/"+model)
	l.mu.Unlock()
	time.Sleep(l.slow)
	if l.err != nil {
		return l.err
	}
	l.server.setAsleep(false)
	return nil
}

func (l *fakeLifecycle) Release(_ context.Context, server, model string) error {
	l.freed = append(l.freed, server+"/"+model)
	return l.err
}

func vllmOn(srv *httptest.Server, l Lifecycle) VLLMEngine {
	return VLLMEngine{Endpoint: srv.URL, Server: "vllm-rig1", Model: vllmModel, Lifecycle: l}
}

func TestGenerateReadsPromptAndGenerationTimingsFromTheStream(t *testing.T) {
	f := &fakeServer{prefill: 150 * time.Millisecond, perToken: 20 * time.Millisecond, tokens: 11}
	reply, err := vllmOn(f.start(t), nil).Generate(context.Background(), vllmModel, "hello", 11)
	if err != nil {
		t.Fatal(err)
	}
	if reply.PromptTokens != 1500 || reply.PromptMs < 140 || reply.PromptMs > 400 {
		t.Errorf("prompt %d tokens in %d ms: the time to the first token is the prompt time", reply.PromptTokens, reply.PromptMs)
	}
	if reply.OutputTokens != 10 || reply.GenerateMs < 150 {
		t.Errorf("generation %d tokens in %d ms: the tokens after the first, over first to last", reply.OutputTokens, reply.GenerateMs)
	}
	if reply.LoadMs != 0 {
		t.Errorf("an awake server has no load time: %d", reply.LoadMs)
	}
}

func TestASleepingModelIsWokenOnlyThroughTheLifecycle(t *testing.T) {
	f := &fakeServer{asleep: true, tokens: 3}
	l := &fakeLifecycle{server: f}
	if _, err := vllmOn(f.start(t), l).Generate(context.Background(), vllmModel, "hello", 3); err != nil {
		t.Fatal(err)
	}
	if len(l.readied) != 1 || l.readied[0] != "vllm-rig1/"+vllmModel {
		t.Errorf("the operator must be asked once to wake it: %v", l.readied)
	}
	if f.wasHit("/wake_up") || f.wasHit("/sleep") {
		t.Errorf("calibration must never wake or sleep a server directly: %v", f.hits)
	}
}

func TestAnAwakeModelAsksNothing(t *testing.T) {
	f := &fakeServer{tokens: 2}
	l := &fakeLifecycle{server: f}
	if _, err := vllmOn(f.start(t), l).Generate(context.Background(), vllmModel, "hello", 2); err != nil {
		t.Fatal(err)
	}
	if len(l.readied) != 0 {
		t.Errorf("woke an awake model: %v", l.readied)
	}
}

func TestAWakeThatFailsOrCannotBeAskedFailsTheProbe(t *testing.T) {
	f := &fakeServer{asleep: true, tokens: 1}
	srv := f.start(t)
	l := &fakeLifecycle{server: f, err: errors.New("the operator refused")}
	if _, err := vllmOn(srv, l).Generate(context.Background(), vllmModel, "x", 1); err == nil ||
		!strings.Contains(err.Error(), "the operator refused") {
		t.Errorf("err = %v", err)
	}
	if _, err := vllmOn(srv, nil).Generate(context.Background(), vllmModel, "x", 1); err == nil ||
		!strings.Contains(err.Error(), "no operator") {
		t.Errorf("a sleeping model with no operator to ask must fail: %v", err)
	}
}

func TestAStreamWithNoTokenIsAnError(t *testing.T) {
	f := &fakeServer{noTokens: true}
	if _, err := vllmOn(f.start(t), nil).Generate(context.Background(), vllmModel, "x", 1); err == nil {
		t.Error("a server that streams nothing has no timings to read")
	}
}

func TestLoadedIsTheModelWhenAwakeWithItsContextAndNothingWhenAsleep(t *testing.T) {
	f := &fakeServer{}
	srv := f.start(t)
	got, err := vllmOn(srv, nil).Loaded(context.Background())
	if err != nil || len(got) != 1 || got[0].Model != vllmModel || got[0].ContextLength != 32768 {
		t.Errorf("awake: %v %v", got, err)
	}
	f.setAsleep(true)
	got, err = vllmOn(srv, nil).Loaded(context.Background())
	if err != nil || len(got) != 0 {
		t.Errorf("asleep: %v %v", got, err)
	}
}

func TestUnloadPutsTheModelToSleepThroughTheLifecycle(t *testing.T) {
	f := &fakeServer{}
	srv := f.start(t)
	l := &fakeLifecycle{server: f}
	if err := vllmOn(srv, l).Unload(context.Background(), vllmModel); err != nil {
		t.Fatal(err)
	}
	if len(l.freed) != 1 || f.wasHit("/sleep") {
		t.Errorf("freed %v, hits %v", l.freed, f.hits)
	}
	if err := vllmOn(srv, nil).Unload(context.Background(), vllmModel); err == nil {
		t.Error("with no operator there is no way to sleep it")
	}
}

func TestConcurrentProbesOfASleepingServerWakeItOnce(t *testing.T) {
	f := &fakeServer{asleep: true, tokens: 2}
	srv := f.start(t)
	l := &fakeLifecycle{server: f, slow: 100 * time.Millisecond}
	e := vllmOn(srv, l)
	var wg sync.WaitGroup
	loads := make(chan int64, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := e.Generate(context.Background(), vllmModel, "hello", 2)
			if err != nil {
				t.Error(err)
			}
			loads <- reply.LoadMs
		}()
	}
	wg.Wait()
	close(loads)
	if len(l.readied) != 1 {
		t.Errorf("the parallel probe must ask for one wake, asked %d times: %v", len(l.readied), l.readied)
	}
	woke := 0
	for ms := range loads {
		if ms > 0 {
			woke++
		}
	}
	if woke != 1 {
		t.Errorf("exactly one request pays the wake, %d reported a load time", woke)
	}
}
