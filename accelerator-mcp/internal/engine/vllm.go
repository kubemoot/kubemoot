/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// wakeTurns holds one lock per server endpoint, so concurrent probes of a sleeping
// server ask the operator to wake it once.
var wakeTurns sync.Map

// fieldModel is the request field that names the model.
const fieldModel = "model"

// Lifecycle makes a model ready or releases it through the operator, the one place
// that wakes and sleeps a vLLM server. Calibration never wakes a server itself.
type Lifecycle interface {
	Ready(ctx context.Context, server, model string) error
	Release(ctx context.Context, server, model string) error
}

// VLLMEngine is the Engine for one vLLM server, which serves one model. It measures
// with streamed /v1/chat/completions requests and holds no model of its own: a
// sleeping model is woken, and put back to sleep, through the Lifecycle.
type VLLMEngine struct {
	Endpoint string
	Server   string
	Model    string
	Client   *http.Client
	// Lifecycle asks the operator to wake or sleep the model; nil never wakes.
	Lifecycle Lifecycle
}

// Generate sends one streamed chat request and reads its timings: the prompt time is
// the time to the first token (the server prefills the whole prompt before it), the
// generation time runs from the first token to the last, and the token counts are the
// server's own usage report. A sleeping model is woken first through the Lifecycle and
// the time that took is the reply's LoadMs.
func (v VLLMEngine) Generate(ctx context.Context, model, prompt string, maxTokens int) (Reply, error) {
	var reply Reply
	woke, err := v.wake(ctx, model)
	if err != nil {
		return reply, err
	}
	reply.LoadMs = woke
	body, err := json.Marshal(map[string]any{
		fieldModel: model, "messages": []map[string]string{{"role": "user", "content": prompt}},
		"max_tokens": maxTokens, "temperature": 0, "stream": true,
		"stream_options":       map[string]any{"include_usage": true},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	if err != nil {
		return reply, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url("/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")
	started := time.Now()
	resp, err := v.client().Do(req)
	if err != nil {
		return reply, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return reply, fmt.Errorf("POST /v1/chat/completions: status %d", resp.StatusCode)
	}
	t, err := readStream(resp.Body, started)
	if err != nil {
		return reply, err
	}
	return t.reply(reply.LoadMs), nil
}

// wake makes the model ready through the Lifecycle when the server reports it asleep,
// and returns how long that took in milliseconds; 0 when it was awake.
func (v VLLMEngine) wake(ctx context.Context, model string) (int64, error) {
	// Requests for one server wake it once: the others wait for the first, then find it awake.
	turn, _ := wakeTurns.LoadOrStore(v.Endpoint, &sync.Mutex{})
	mu := turn.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	sleeping, err := v.isSleeping(ctx)
	if err != nil || !sleeping {
		return 0, err
	}
	if v.Lifecycle == nil {
		return 0, fmt.Errorf("%s is asleep and no operator is available to wake it", model)
	}
	started := time.Now()
	if err := v.Lifecycle.Ready(ctx, v.Server, model); err != nil {
		return 0, fmt.Errorf("waking %s: %w", model, err)
	}
	return time.Since(started).Milliseconds(), nil
}

// Loaded lists the model when its server is awake, with the context length the
// server reports; a sleeping server holds nothing loaded.
func (v VLLMEngine) Loaded(ctx context.Context) ([]Resident, error) {
	sleeping, err := v.isSleeping(ctx)
	if err != nil || sleeping {
		return nil, err
	}
	return []Resident{{Model: v.Model, ContextLength: v.contextLength(ctx)}}, nil
}

// Unload puts the model to sleep through the Lifecycle.
func (v VLLMEngine) Unload(ctx context.Context, model string) error {
	if v.Lifecycle == nil {
		return errors.New("no operator is available to put the model to sleep")
	}
	return v.Lifecycle.Release(ctx, v.Server, model)
}

// isSleeping reads /is_sleeping; a server without the sleep endpoints (404) is awake.
func (v VLLMEngine) isSleeping(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url("/is_sleeping"), nil)
	if err != nil {
		return false, err
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET /is_sleeping: status %d", resp.StatusCode)
	}
	var out struct {
		IsSleeping bool `json:"is_sleeping"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.IsSleeping, nil
}

// contextLength is the model's max_model_len from /v1/models; 0 when unreadable.
func (v VLLMEngine) contextLength(ctx context.Context) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url("/v1/models"), nil)
	if err != nil {
		return 0
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return 0
	}
	for _, m := range out.Data {
		if m.ID == v.Model {
			return m.MaxModelLen
		}
	}
	return 0
}

func (v VLLMEngine) url(path string) string {
	return strings.TrimRight(v.Endpoint, "/") + path
}

func (v VLLMEngine) client() *http.Client {
	if v.Client != nil {
		return v.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// streamTimes is what a streamed completion showed.
type streamTimes struct {
	firstToken, last time.Time
	started          time.Time
	promptTokens     int64
	completionTokens int64
}

// streamChunk is one server-sent event of a streamed completion.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// hasToken is true when the chunk carries generated text, whether the server calls it
// content or reasoning.
func (c streamChunk) hasToken() bool {
	for _, ch := range c.Choices {
		d := ch.Delta
		if d.Content != "" || d.ReasoningContent != "" || d.Reasoning != "" {
			return true
		}
	}
	return false
}

// observe folds one line of the stream into the times.
func (t *streamTimes) observe(line string, now time.Time) {
	data, ok := strings.CutPrefix(line, "data: ")
	if !ok || data == "[DONE]" {
		return
	}
	var chunk streamChunk
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return
	}
	if chunk.hasToken() {
		if t.firstToken.IsZero() {
			t.firstToken = now
		}
		t.last = now
	}
	if chunk.Usage != nil {
		t.promptTokens, t.completionTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
	}
}

// readStream reads the server-sent events of a streamed completion.
func readStream(r io.Reader, started time.Time) (streamTimes, error) {
	t := streamTimes{started: started}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		t.observe(scanner.Text(), time.Now())
	}
	if err := scanner.Err(); err != nil {
		return t, err
	}
	if t.firstToken.IsZero() {
		return t, errors.New("the server streamed no token")
	}
	return t, nil
}

// reply converts the stream into the timings calibration records. Tokens after the
// first are the ones the generation time covers.
func (t streamTimes) reply(loadMs int64) Reply {
	generated := max(t.completionTokens-1, 0)
	return Reply{
		LoadMs:       loadMs,
		PromptTokens: t.promptTokens,
		PromptMs:     t.firstToken.Sub(t.started).Milliseconds(),
		OutputTokens: generated,
		GenerateMs:   t.last.Sub(t.firstToken).Milliseconds(),
		TotalMs:      t.last.Sub(t.started).Milliseconds(),
	}
}
