/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_embedding.go upgrades scenario self-consistency from lexical word
// overlap to SEMANTIC similarity: it embeds each iteration's synthesis with the
// cluster's embedding model (nomic-embed-text via Ollama, the same one RAG uses)
// and scores consistency as the mean pairwise cosine of those vectors. This
// rewards "same meaning, different number" — so a live-metric scenario whose
// answer legitimately changes run-to-run ("GPU at 41%" vs "GPU at 76%") is no
// longer punished the way bag-of-words overlap punished it.
//
// The work is cached per run in a sidecar Object Store entry, so only the first
// report generation embeds; later downloads read the cached scores. If no
// embedding model is reachable, the caller falls back to the lexical score —
// reports never fail because the embedder is down.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// embedder turns text into a vector. Implemented by ollamaEmbedder; faked in tests.
type embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// ollamaEmbedder calls an Ollama-compatible /api/embeddings endpoint (the
// endpoint + model come from a ready EmbeddingModel CR).
type ollamaEmbedder struct {
	url    string
	model  string
	client *http.Client
}

func newOllamaEmbedder(endpoint, model string) *ollamaEmbedder {
	if model == "" {
		model = "nomic-embed-text"
	}
	return &ollamaEmbedder{
		url:    strings.TrimRight(endpoint, "/") + "/api/embeddings",
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *ollamaEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	body, err := json.Marshal(map[string]string{"model": o.model, "prompt": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings endpoint returned %d", resp.StatusCode)
	}
	var parsed struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if len(parsed.Embedding) == 0 {
		return nil, fmt.Errorf("embeddings endpoint returned an empty vector")
	}
	return parsed.Embedding, nil
}

// cosineVec is cosine similarity between two equal-length vectors (0 on
// mismatch or a zero vector).
func cosineVec(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// semanticConsistency embeds a scenario's synthesis texts and returns the mean
// pairwise cosine similarity as 0-100. Fewer than two usable texts → 100
// (nothing to contradict). Any embedding failure is returned so the caller can
// fall back to the lexical score for the whole run (keeping one method per
// report rather than a mix).
func semanticConsistency(ctx context.Context, texts []string, emb embedder) (float64, error) {
	vecs := make([][]float64, 0, len(texts))
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		v, err := emb.Embed(ctx, t)
		if err != nil {
			return 0, err
		}
		vecs = append(vecs, v)
	}
	if len(vecs) < 2 {
		return 100, nil
	}
	var sum float64
	var pairs int
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			sum += cosineVec(vecs[i], vecs[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 100, nil
	}
	return sum / float64(pairs) * 100, nil
}
