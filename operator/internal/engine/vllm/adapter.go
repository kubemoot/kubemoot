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

// Package vllm is the engine adapter for vLLM servers that someone else runs.
// vLLM's sleep mode serves one model per server, so a provider is a set of
// servers: one OpenAI-compatible endpoint per served model, all but one of them
// asleep on a shared accelerator. The adapter reads each server's /health,
// /v1/models, /is_sleeping and /metrics, makes a model ready by waking its
// server (after putting any other awake server of the provider to sleep), and
// releases a model by putting its server to sleep. It never restarts a server:
// a restart takes 34 to 47 s where a wake takes 0.7 to 4.9 s.
package vllm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	pathHealth      = "/health"
	pathModels      = "/v1/models"
	pathIsSleeping  = "/is_sleeping"
	pathMetrics     = "/metrics"
	pathSleep       = "/sleep"
	pathWakeUp      = "/wake_up"
	pathCollective  = "/collective_rpc"
	contentTypeJSON = "application/json"

	// maxBody caps what is read from any answer; a /metrics page is the largest.
	maxBody = 8 << 20
)

// Adapter talks to the vLLM servers of one provider over HTTP.
type Adapter struct {
	client *http.Client
}

var _ engine.Adapter = (*Adapter)(nil)

// New returns an Adapter that calls vLLM with client.
func New(client *http.Client) *Adapter {
	return &Adapter{client: client}
}

// Factory is the engine.Factory for vLLM.
func Factory(client *http.Client) engine.Adapter {
	return New(client)
}

// statusError is an answer with a status other than the one expected.
type statusError struct {
	path string
	code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("vLLM %s returned status %d", e.path, e.code)
}

// isNotFound reports whether err is a 404, which is how a server started
// without sleep mode answers the sleep endpoints.
func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

// serversOf is the servers the spec lists; none when the spec has no vllm block.
func serversOf(spec aiv1alpha1.ModelProviderSpec) []aiv1alpha1.VLLMServer {
	return spec.Servers
}

// serverFor is the server that serves model.
func serverFor(spec aiv1alpha1.ModelProviderSpec, model string) (aiv1alpha1.VLLMServer, error) {
	for _, s := range serversOf(spec) {
		if s.Model == model {
			return s, nil
		}
	}
	return aiv1alpha1.VLLMServer{}, fmt.Errorf("no vLLM server serves model %q", model)
}

// Ready wakes the server of model. Any other server of the provider that is
// awake is put to sleep first, so the accelerator has room; a server that does
// not answer holds nothing and is left alone. A model whose server is already
// awake, or has no sleep endpoints, needs nothing.
func (a *Adapter) Ready(ctx context.Context, spec aiv1alpha1.ModelProviderSpec, model string) error {
	target, err := serverFor(spec, model)
	if err != nil {
		return err
	}
	level := spec.SleepLevelOrDefault()
	for _, other := range byEndpoint(serversOf(spec)) {
		if other.endpoint == target.Endpoint {
			continue // the target's own server, whatever alias it is asked for by
		}
		if err := a.sleepIfAwake(ctx, aiv1alpha1.VLLMServer{Model: other.models[0], Endpoint: other.endpoint}, level); err != nil {
			return err
		}
	}
	sleeping, err := a.isSleeping(ctx, target.Endpoint)
	if isNotFound(err) {
		return nil // no sleep endpoints: the server is always awake
	}
	if err != nil {
		return fmt.Errorf("reading the sleep state of %q: %w", model, err)
	}
	if !sleeping {
		return nil
	}
	return a.wake(ctx, target.Endpoint, level)
}

// Release puts the server of model to sleep at the provider's sleep level. A
// server already asleep is left as it is.
func (a *Adapter) Release(ctx context.Context, spec aiv1alpha1.ModelProviderSpec, model string) error {
	target, err := serverFor(spec, model)
	if err != nil {
		return err
	}
	sleeping, err := a.isSleeping(ctx, target.Endpoint)
	if err != nil {
		return fmt.Errorf("reading the sleep state of %q: %w", model, err)
	}
	if sleeping {
		return nil
	}
	return a.sleep(ctx, target.Endpoint, spec.SleepLevelOrDefault())
}

// sleepIfAwake puts srv to sleep when it is awake. A server that is down (its
// /health does not answer either) holds no accelerator memory and is skipped. A
// server that answers but has no sleep endpoints cannot give up the accelerator,
// and one whose sleep state cannot be read might still hold it; both are errors.
func (a *Adapter) sleepIfAwake(ctx context.Context, srv aiv1alpha1.VLLMServer, level int) error {
	sleeping, err := a.isSleeping(ctx, srv.Endpoint)
	switch {
	case isNotFound(err):
		return fmt.Errorf("the vLLM server for %q is awake but has no sleep mode: start it with --enable-sleep-mode and VLLM_SERVER_DEV_MODE=1", srv.Model)
	case err != nil:
		if _, healthErr := a.get(ctx, srv.Endpoint, pathHealth); healthErr != nil {
			return nil
		}
		return fmt.Errorf("reading the sleep state of %q: %w", srv.Model, err)
	case sleeping:
		return nil
	}
	if err := a.sleep(ctx, srv.Endpoint, level); err != nil {
		return fmt.Errorf("putting %q to sleep: %w", srv.Model, err)
	}
	return nil
}

// sleep asks the server to sleep at level.
func (a *Adapter) sleep(ctx context.Context, endpoint string, level int) error {
	return a.post(ctx, endpoint, fmt.Sprintf("%s?level=%d", pathSleep, level), nil)
}

// wake brings a sleeping server back. After a level 2 sleep the weights are gone:
// restore the weight buffers, reload the weights from disk, then restore the
// key-value cache, in that order. After a level 1 sleep one call restores
// everything. The kubemoot-vllm chart's launcher wakes the first server it starts
// the same way (vllm/charts/kubemoot-vllm/files/launcher.py, Server.wake); keep the
// two in step.
func (a *Adapter) wake(ctx context.Context, endpoint string, level int) error {
	if level == 2 {
		return a.wakeAfterLevelTwo(ctx, endpoint)
	}
	return a.post(ctx, endpoint, pathWakeUp, nil)
}

// wakeAfterLevelTwo is the three requests that restore a server that dropped its weights.
func (a *Adapter) wakeAfterLevelTwo(ctx context.Context, endpoint string) error {
	steps := []struct {
		path string
		body []byte
	}{
		{pathWakeUp + "?tags=weights", nil},
		{pathCollective, []byte(`{"method":"reload_weights"}`)},
		{pathWakeUp + "?tags=kv_cache", nil},
	}
	for _, s := range steps {
		if err := a.post(ctx, endpoint, s.path, s.body); err != nil {
			return err
		}
	}
	return nil
}

// isSleeping reads /is_sleeping. A server without sleep mode answers 404; the
// caller decides what that means.
func (a *Adapter) isSleeping(ctx context.Context, endpoint string) (bool, error) {
	body, err := a.get(ctx, endpoint, pathIsSleeping)
	if err != nil {
		return false, err
	}
	var out struct {
		IsSleeping bool `json:"is_sleeping"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, fmt.Errorf("decode vLLM %s: %w", pathIsSleeping, err)
	}
	return out.IsSleeping, nil
}

// get GETs endpoint+path and returns a 200 answer's body.
func (a *Adapter) get(ctx context.Context, endpoint, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, join(endpoint, path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{path: path, code: resp.StatusCode}
	}
	return body, nil
}

// post POSTs body (JSON when present) to endpoint+path and wants a 2xx answer.
func (a *Adapter) post(ctx context.Context, endpoint, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, join(endpoint, path), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", contentTypeJSON)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode/100 != 2 {
		return &statusError{path: path, code: resp.StatusCode}
	}
	return nil
}

// join appends path (which may carry a query) to a server's base URL.
func join(endpoint, path string) string {
	return strings.TrimRight(endpoint, "/") + path
}

// ServedModel is one model a vLLM server lists on /v1/models.
type ServedModel struct {
	// ID is the served name (--served-model-name).
	ID string
	// Root is where the server loaded the weights from: the Hugging Face
	// repository, or a path. Empty when the server does not say.
	Root string
}

// ServedModels asks the server at endpoint which models it serves.
func (a *Adapter) ServedModels(ctx context.Context, endpoint string) ([]ServedModel, error) {
	body, err := a.get(ctx, endpoint, pathModels)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID   string `json:"id"`
			Root string `json:"root"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode vLLM %s: %w", pathModels, err)
	}
	models := make([]ServedModel, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, ServedModel{ID: m.ID, Root: m.Root})
	}
	return models, nil
}
