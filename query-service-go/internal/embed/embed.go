// Package embed turns a question into a unit-length vector through an
// OpenAI-compatible embeddings endpoint, the API every model server serves.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	embedTimeout = 60 * time.Second
	pingTimeout  = 5 * time.Second
	maxBody      = 64 << 20
)

// UnavailableError means the embedding server could not be reached or refused the
// request. The API answers it with 502; any other embedding error is a 500.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return "Embedding service error: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// Client calls embedding servers; one client serves every source.
type Client struct {
	HTTP   *http.Client
	APIKey string
}

// NewClient returns a client with the service's timeouts. apiKey is sent as a bearer
// token when set.
func NewClient(apiKey string) *Client {
	return &Client{HTTP: &http.Client{Timeout: embedTimeout}, APIKey: apiKey}
}

type request struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type response struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Embed returns text's embedding, scaled to unit length.
func (c *Client) Embed(ctx context.Context, endpoint, model, text string) ([]float64, error) {
	body, err := json.Marshal(request{Model: model, Input: text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &UnavailableError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := c.do(req)
	if err != nil {
		return nil, err
	}
	return decode(raw)
}

func decode(raw []byte) ([]float64, error) {
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embedding response is not JSON: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, errors.New("embedding response has no data")
	}
	return L2Normalize(out.Data[0].Embedding), nil
}

// Ping checks that the embedding server answers on the OpenAI models route.
func (c *Client) Ping(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/models", nil)
	if err != nil {
		return &UnavailableError{Err: err}
	}
	_, err = c.do(req)
	return err
}

// do sends the request and returns the body of a 2xx answer; a transport failure or a
// non-2xx status is an UnavailableError.
func (c *Client) do(req *http.Request) ([]byte, error) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &UnavailableError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil, &UnavailableError{Err: fmt.Errorf("server answered %s for %s", resp.Status, req.URL.Path)}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, &UnavailableError{Err: err}
	}
	return raw, nil
}

// L2Normalize scales a vector to unit length. Servers differ (Ollama answers unit
// vectors, vLLM the raw model output); a zero vector has no direction and is returned
// as it is.
func L2Normalize(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return v
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}
