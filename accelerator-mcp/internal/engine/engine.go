/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package engine talks to a model server for the accelerator tools: send one
// request and read the engine's own timings, see what is loaded, and unload a model.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Engine is what calibration needs from a model server: send one request and read
// its own timings, see what is loaded, and unload a model.
type Engine interface {
	Generate(ctx context.Context, model, prompt string, maxTokens int) (Reply, error)
	Loaded(ctx context.Context) ([]Resident, error)
	Unload(ctx context.Context, model string) error
}

// EmbeddingChecker is an Engine that can say a model only embeds, so it cannot be
// asked to generate. An engine without it reports no such models.
type EmbeddingChecker interface {
	EmbeddingOnly(ctx context.Context, model string) (bool, error)
}

// Reply is one request's outcome as the engine timed it. Times are milliseconds.
type Reply struct {
	LoadMs       int64
	PromptTokens int64
	PromptMs     int64
	OutputTokens int64
	GenerateMs   int64
	TotalMs      int64
}

// Resident is a model the engine has loaded: its size and how much of it is on
// the accelerator. VRAMMiB below SizeMiB means part of it spilled to the CPU.
type Resident struct {
	Model         string
	SizeMiB       int64
	VRAMMiB       int64
	ContextLength int
}

// OllamaEngine is the Engine for an Ollama server.
type OllamaEngine struct {
	Endpoint string
	Client   *http.Client
}

const mib = 1024 * 1024

// Generate sends one non-streaming /api/generate request.
func (o OllamaEngine) Generate(ctx context.Context, model, prompt string, maxTokens int) (Reply, error) {
	body := map[string]any{
		"model":   model,
		"prompt":  prompt,
		"stream":  false,
		"options": map[string]any{"num_predict": maxTokens, "temperature": 0},
	}
	var out struct {
		LoadDuration       int64 `json:"load_duration"`
		PromptEvalCount    int64 `json:"prompt_eval_count"`
		PromptEvalDuration int64 `json:"prompt_eval_duration"`
		EvalCount          int64 `json:"eval_count"`
		EvalDuration       int64 `json:"eval_duration"`
		TotalDuration      int64 `json:"total_duration"`
	}
	if err := o.post(ctx, "/api/generate", body, &out); err != nil {
		return Reply{}, err
	}
	return Reply{
		LoadMs:       nanosToMs(out.LoadDuration),
		PromptTokens: out.PromptEvalCount,
		PromptMs:     nanosToMs(out.PromptEvalDuration),
		OutputTokens: out.EvalCount,
		GenerateMs:   nanosToMs(out.EvalDuration),
		TotalMs:      nanosToMs(out.TotalDuration),
	}, nil
}

// Loaded reads /api/ps.
func (o OllamaEngine) Loaded(ctx context.Context) ([]Resident, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Endpoint+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []struct {
			Name          string `json:"name"`
			Size          int64  `json:"size"`
			SizeVRAM      int64  `json:"size_vram"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := o.do(req, &out); err != nil {
		return nil, err
	}
	residents := make([]Resident, 0, len(out.Models))
	for _, m := range out.Models {
		residents = append(residents, Resident{Model: m.Name, SizeMiB: m.Size / mib, VRAMMiB: m.SizeVRAM / mib,
			ContextLength: m.ContextLength})
	}
	return residents, nil
}

// EmbeddingOnly asks /api/show whether the model lists the "embedding" capability
// and not "completion". An Ollama that lists no capabilities cannot say, so such a
// model reports false.
func (o OllamaEngine) EmbeddingOnly(ctx context.Context, model string) (bool, error) {
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := o.post(ctx, "/api/show", map[string]any{fieldModel: model}, &out); err != nil {
		return false, err
	}
	var completion, embedding bool
	for _, c := range out.Capabilities {
		completion = completion || c == "completion"
		embedding = embedding || c == "embedding"
	}
	return embedding && !completion, nil
}

// Unload asks Ollama to drop the model now (keep_alive 0).
func (o OllamaEngine) Unload(ctx context.Context, model string) error {
	return o.post(ctx, "/api/generate", map[string]any{"model": model, "keep_alive": 0}, nil)
}

func (o OllamaEngine) post(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return o.do(req, out)
}

func (o OllamaEngine) do(req *http.Request, out any) error {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func nanosToMs(n int64) int64 {
	return n / int64(time.Millisecond)
}
