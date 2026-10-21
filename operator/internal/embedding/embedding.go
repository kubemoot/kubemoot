/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package embedding calls the OpenAI-compatible /v1/embeddings API that Ollama,
// vLLM and other model servers serve, and returns unit-length vectors. One client
// for every engine keeps the vectors an engine produces comparable with another's.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

const (
	path    = "/v1/embeddings"
	maxBody = 64 << 20
)

// ErrNotEmbedding means the server answered, and does not serve the model as an
// embedding model (a chat model, or an unknown name). It is a definite answer, not
// a failure to reach the server.
var ErrNotEmbedding = errors.New("the server does not serve the model for embeddings")

// Embed returns the unit-length embedding of input from the server at endpoint.
func Embed(ctx context.Context, client *http.Client, endpoint, model, input string) ([]float32, error) {
	body, err := json.Marshal(map[string]any{"model": model, "input": input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp.StatusCode); err != nil {
		return nil, err
	}
	return decode(raw)
}

// checkStatus maps an HTTP status to nil, ErrNotEmbedding (the server refused this
// model or API), or a plain error (the server could not answer now).
func checkStatus(code int) error {
	switch {
	case code/100 == 2:
		return nil
	case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests, code/100 == 5 && code != http.StatusNotImplemented:
		return fmt.Errorf("embeddings endpoint returned %d", code)
	case code/100 == 4, code == http.StatusNotImplemented:
		return fmt.Errorf("%w (status %d)", ErrNotEmbedding, code)
	}
	return fmt.Errorf("embeddings endpoint returned %d", code)
}

// decode reads the first vector of an /v1/embeddings answer and normalizes it.
func decode(raw []byte) ([]float32, error) {
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode embeddings answer: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, errors.New("embeddings endpoint returned an empty vector")
	}
	return Normalize(parsed.Data[0].Embedding), nil
}

// Normalize scales v to unit length in place and returns it. A zero vector has no
// direction and is returned unchanged.
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
	return v
}
