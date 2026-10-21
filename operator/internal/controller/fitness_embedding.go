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
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/embedding"
)

// embedder turns text into a vector. Implemented by serverEmbedder; faked in tests.
type embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
}

// serverEmbedder calls the OpenAI-compatible /v1/embeddings API of the server an
// EmbeddingModel is bound to (the endpoint and model come from a ready
// EmbeddingModel CR), whatever engine serves it.
type serverEmbedder struct {
	endpoint string
	model    string
	client   *http.Client
}

func newServerEmbedder(endpoint, model string) *serverEmbedder {
	if model == "" {
		model = "nomic-embed-text"
	}
	return &serverEmbedder{endpoint: endpoint, model: model, client: &http.Client{Timeout: 30 * time.Second}}
}

func (o *serverEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	v, err := embedding.Embed(ctx, o.client, o.endpoint, o.model, text)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out, nil
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
