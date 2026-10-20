/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package storetest holds in-memory fakes of the KV and the engine for tests.
package storetest

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/kubemoot/kubemoot/accelerator-mcp/internal/engine"
)

// MemKV is an in-memory KV. Set FailGet, FailPut, FailList, or FailDelete to make
// that operation return ErrKV.
type MemKV struct {
	mu         sync.Mutex
	data       map[string][]byte
	FailGet    bool
	FailPut    bool
	FailList   bool
	FailDelete bool
	// ListErr, when set, is what ListKVKeys returns instead of ErrKV, to stand in for
	// a bucket that does not exist or a NATS that cannot be reached.
	ListErr error
}

// ErrKV is the error a failing MemKV operation returns.
var ErrKV = errors.New("kv unavailable")

// NewMemKV returns an empty MemKV.
func NewMemKV() *MemKV { return &MemKV{data: map[string][]byte{}} }

// GetKVValue returns the value or nil.
func (m *MemKV) GetKVValue(bucket, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailGet {
		return nil, ErrKV
	}
	return m.data[bucket+"/"+key], nil
}

// PutKVValue stores the value.
func (m *MemKV) PutKVValue(bucket, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut {
		return ErrKV
	}
	m.data[bucket+"/"+key] = value
	return nil
}

// ListKVKeys lists the bucket's keys, sorted.
func (m *MemKV) ListKVKeys(bucket string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	if m.FailList {
		return nil, ErrKV
	}
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, bucket+"/") {
			keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// DeleteKVKey deletes the key.
func (m *MemKV) DeleteKVKey(bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailDelete {
		return ErrKV
	}
	delete(m.data, bucket+"/"+key)
	return nil
}

// Keys lists the bucket's keys, ignoring the failure switches.
func (m *MemKV) Keys(bucket string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, bucket+"/") {
			keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	sort.Strings(keys)
	return keys
}

// Set flips a failure switch under the lock, for tests that change it mid-run.
func (m *MemKV) Set(failure *bool, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*failure = on
}

// ErrEngine is the error a failing Engine returns.
var ErrEngine = errors.New("engine down")

// Engine scripts a model server: loading a model makes it resident with
// VRAMFraction of its size on the accelerator; every reply generates at 50
// tokens per second and processes prompts at 10,000 tokens per second.
type Engine struct {
	mu           sync.Mutex
	Resident     map[string]engine.Resident
	Sizes        map[string]int64
	VRAMFraction float64
	Fail         bool
	FailUnload   bool
	OnGenerate   func(n int)
	Generates    int
	Unloads      []string
}

// NewEngine returns an Engine with nothing loaded that keeps models whole on the accelerator.
func NewEngine() *Engine {
	return &Engine{Resident: map[string]engine.Resident{}, Sizes: map[string]int64{}, VRAMFraction: 1}
}

// Generate loads the model if needed and replies.
func (e *Engine) Generate(_ context.Context, model, prompt string, maxTokens int) (engine.Reply, error) {
	e.mu.Lock()
	e.Generates++
	n, hook, fail := e.Generates, e.OnGenerate, e.Fail
	_, wasLoaded := e.Resident[model]
	if !fail && !wasLoaded {
		size := e.Sizes[model]
		e.Resident[model] = engine.Resident{Model: model, SizeMiB: size,
			VRAMMiB: int64(float64(size) * e.VRAMFraction), ContextLength: 8192}
	}
	e.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if fail {
		return engine.Reply{}, ErrEngine
	}
	reply := engine.Reply{PromptTokens: int64(len(prompt) / 4), PromptMs: int64(len(prompt) / 40),
		OutputTokens: int64(maxTokens), GenerateMs: int64(maxTokens) * 20, TotalMs: 100}
	if !wasLoaded {
		reply.LoadMs = 3000
	}
	return reply, nil
}

// Loaded lists the resident models.
func (e *Engine) Loaded(context.Context) ([]engine.Resident, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]engine.Resident, 0, len(e.Resident))
	for _, r := range e.Resident {
		out = append(out, r)
	}
	return out, nil
}

// Unload drops the model.
func (e *Engine) Unload(_ context.Context, model string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.FailUnload {
		return ErrEngine
	}
	delete(e.Resident, model)
	e.Unloads = append(e.Unloads, model)
	return nil
}
