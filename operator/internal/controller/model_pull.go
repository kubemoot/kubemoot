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

package controller

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

	"k8s.io/apimachinery/pkg/types"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// pullStallLimit cancels a pull whose stream goes silent. It is a safety net for a
// dead connection; a healthy pull reports progress many times a second.
const pullStallLimit = 10 * time.Minute

// errNoSpace marks a pull that needs more room than the provider has.
var errNoSpace = errors.New("not enough free disk")

// pullKey identifies one download: the provider and the model tag. Models that
// share both share the download.
func pullKey(providerRef, tag string) string { return providerRef + "/" + tag }

// pullState is one pull running outside the reconcile loop, or its outcome.
type pullState struct {
	cancel context.CancelFunc
	owner  types.UID

	mu       sync.Mutex
	layers   map[string]*pullLayer
	finished bool
	err      error
	// freeAtFailure is the provider's free disk when the pull failed for lack of
	// space; the pull is tried again once the free disk differs.
	freeAtFailure int64
}

// pullLayer is the progress of one layer of a model download.
type pullLayer struct{ Total, Completed int64 }

func (p *pullState) setLayer(digest string, total, completed int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.layers[digest] = &pullLayer{Total: total, Completed: completed}
}

// totals sums the layers announced so far.
func (p *pullState) totals() (completed, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.layers {
		total += l.Total
		completed += l.Completed
	}
	return completed, total
}

func (p *pullState) finish(err error, freeBytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finished = true
	p.err = err
	if errors.Is(err, errNoSpace) {
		p.freeAtFailure = freeBytes
	}
}

// pullOutcome is how far a pull got.
type pullOutcome struct {
	finished      bool
	err           error
	freeAtFailure int64
}

func (p *pullState) outcome() pullOutcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pullOutcome{finished: p.finished, err: p.err, freeAtFailure: p.freeAtFailure}
}

// pullTracker holds the pulls this operator process has started. A restarted
// operator starts empty; a Model still Pulling then starts its pull again and
// Ollama resumes the partial download.
type pullTracker struct {
	mu    sync.Mutex
	pulls map[string]*pullState
}

func (t *pullTracker) get(key string) *pullState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pulls[key]
}

// ownedBy lists the keys of pulls started for the Model with the given UID.
func (t *pullTracker) ownedBy(owner types.UID) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var keys []string
	for k, p := range t.pulls {
		if p.owner == owner {
			keys = append(keys, k)
		}
	}
	return keys
}

// drop forgets a pull and cancels it when it is still running.
func (t *pullTracker) drop(key string) {
	t.mu.Lock()
	p := t.pulls[key]
	delete(t.pulls, key)
	t.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

// pullStream is the pull request, the stream settings and what ends the stream.
type pullStream struct {
	endpoint  string
	tag       string
	freeBytes int64 // provider free disk when known, otherwise 0
	client    *http.Client
}

// startPull begins a streaming pull in a goroutine and returns at once. The check
// for a pull already tracked under key and the insert happen under one lock, so
// concurrent reconciles of Models sharing a tag start one pull. owner is the UID
// of the Model that asked for it.
func startPull(tracker *pullTracker, key string, owner types.UID, ps pullStream) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.pulls[key] != nil {
		return
	}
	if tracker.pulls == nil {
		tracker.pulls = map[string]*pullState{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := &pullState{cancel: cancel, owner: owner, layers: map[string]*pullLayer{}}
	tracker.pulls[key] = state
	go func() {
		defer cancel()
		state.finish(ps.run(ctx, cancel, state), ps.freeBytes)
	}()
}

// run streams the pull and records progress on state. It returns nil on success.
func (ps pullStream) run(ctx context.Context, cancel context.CancelFunc, state *pullState) error {
	body, err := json.Marshal(OllamaPullRequest{Name: ps.tag, Stream: true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ps.endpoint+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	streamClient := *ps.client
	streamClient.Timeout = 0
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ps.statusError(resp)
	}
	stall := time.AfterFunc(pullStallLimit, cancel)
	defer stall.Stop()
	return ps.readStream(ctx, resp.Body, state, stall)
}

// statusError turns a non-200 pull response into a message that names the cause.
func (ps pullStream) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := strings.TrimSpace(string(raw))
	if isNoSpaceText(detail) {
		return ps.noSpaceError(0)
	}
	if detail == "" {
		return fmt.Errorf("pull failed with status %d", resp.StatusCode)
	}
	return fmt.Errorf("pull failed with status %d: %s", resp.StatusCode, detail)
}

func isNoSpaceText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no space left") || strings.Contains(s, "disk full")
}

// noSpaceError describes a pull that cannot fit; needBytes is 0 when unknown.
func (ps pullStream) noSpaceError(needBytes int64) error {
	switch {
	case needBytes > 0 && ps.freeBytes > 0:
		return fmt.Errorf("%w: %s needs %s more and the provider has %s free", errNoSpace, ps.tag, formatBytes(needBytes), formatBytes(ps.freeBytes))
	case ps.freeBytes > 0:
		return fmt.Errorf("%w: the provider ran out of disk pulling %s (%s free before the pull)", errNoSpace, ps.tag, formatBytes(ps.freeBytes))
	default:
		return fmt.Errorf("%w: the provider ran out of disk pulling %s", errNoSpace, ps.tag)
	}
}

// pullLine is one JSON line of the /api/pull stream.
type pullLine struct {
	OllamaPullResponse
	Error string `json:"error,omitempty"`
}

// readStream records each progress line and ends on success, an error line, a
// cancelled context or a stream that closes early.
func (ps pullStream) readStream(ctx context.Context, body io.Reader, state *pullState, stall *time.Timer) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		stall.Reset(pullStallLimit)
		var line pullLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		done, err := ps.applyLine(line, state)
		if err != nil || done {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("pull stopped: %w", err)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("pull stream broke: %w", err)
	}
	return errors.New("pull stream ended before the model finished downloading")
}

// applyLine folds one stream line into state. done is true on success.
func (ps pullStream) applyLine(line pullLine, state *pullState) (done bool, err error) {
	if line.Error != "" {
		if isNoSpaceText(line.Error) {
			return false, ps.noSpaceError(0)
		}
		return false, fmt.Errorf("pull failed: %s", line.Error)
	}
	if line.Status == "success" {
		return true, nil
	}
	if line.Digest == "" || line.Total <= 0 {
		return false, nil
	}
	state.setLayer(line.Digest, line.Total, line.Completed)
	return false, ps.checkFits(state)
}

// checkFits fails the pull when the bytes still to download exceed the free disk.
func (ps pullStream) checkFits(state *pullState) error {
	if ps.freeBytes <= 0 {
		return nil
	}
	completed, total := state.totals()
	if need := total - completed; need > ps.freeBytes {
		return ps.noSpaceError(need - ps.freeBytes)
	}
	return nil
}

// pullProgress converts the tracker's byte counts to the status field.
func pullProgress(completed, total int64) *aiv1alpha1.PullProgress {
	p := &aiv1alpha1.PullProgress{CompletedBytes: completed, TotalBytes: total}
	if total > 0 {
		p.Percent = int32(completed * 100 / total)
	}
	return p
}

// pullMessage is the status message for a pull in progress.
func pullMessage(p *aiv1alpha1.PullProgress) string {
	if p.TotalBytes <= 0 {
		return "Pull started, waiting for the registry"
	}
	return fmt.Sprintf("Pulling model: %d%% (%s of %s)", p.Percent, formatBytes(p.CompletedBytes), formatBytes(p.TotalBytes))
}
