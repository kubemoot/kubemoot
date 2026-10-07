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
	"reflect"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	owners map[types.UID]bool
	// provider names the ModelProvider the pull downloads onto.
	provider string

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

// addOwner records another object that wants the pull, so the pull is released
// only when none of its owners still wants it.
func (p *pullState) addOwner(owner types.UID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.owners[owner] = true
}

func (p *pullState) hasOwner(owner types.UID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owners[owner]
}

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

// succeeded reports whether the pull finished and the model downloaded.
func (p *pullState) succeeded() bool {
	out := p.outcome()
	return out.finished && out.err == nil
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

// PullTracker holds the pulls this operator process has started, shared by every
// controller that downloads models. A restarted operator starts empty; a model still
// Pulling then starts its pull again and Ollama resumes the partial download.
//
// Ollama has no API that removes the partial files of an unfinished model, and it
// prunes them only when the server starts. The tracker therefore also remembers the
// bytes a cancelled or failed pull left on disk, so the provider's free-disk
// estimate can count them until the model server restarts.
type PullTracker struct {
	mu        sync.Mutex
	pulls     map[string]*pullState
	leftovers map[string]leftover
	instances map[string]string
}

// leftover is the partial download a stopped pull left on a provider's disk.
type leftover struct {
	provider string
	bytes    int64
}

func (t *PullTracker) get(key string) *pullState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pulls[key]
}

// ownedBy lists the keys of pulls started for the object with the given UID.
func (t *PullTracker) ownedBy(owner types.UID) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var keys []string
	for k, p := range t.pulls {
		if p.hasOwner(owner) {
			keys = append(keys, k)
		}
	}
	return keys
}

// forget drops a pull whose model is on the provider, and any partial record. A
// pull still running is cancelled.
func (t *PullTracker) forget(key string) {
	t.mu.Lock()
	p := t.pulls[key]
	delete(t.pulls, key)
	delete(t.leftovers, key)
	t.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

// abandon stops a pull that did not finish and remembers the bytes it downloaded,
// which stay on the provider's disk as partial files.
func (t *PullTracker) abandon(key string) {
	t.mu.Lock()
	p := t.pulls[key]
	delete(t.pulls, key)
	if p != nil && !p.succeeded() {
		if completed, _ := p.totals(); completed > 0 {
			if t.leftovers == nil {
				t.leftovers = map[string]leftover{}
			}
			t.leftovers[key] = leftover{provider: p.provider, bytes: completed}
		}
	}
	t.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

// PartialBytes is the disk the provider holds for unfinished downloads: pulls in
// progress plus the partial files of stopped ones.
func (t *PullTracker) PartialBytes(provider string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var sum int64
	for _, l := range t.leftovers {
		if l.provider == provider {
			sum += l.bytes
		}
	}
	for _, p := range t.pulls {
		if p.provider == provider && !p.succeeded() {
			completed, _ := p.totals()
			sum += completed
		}
	}
	return sum
}

// ServerRestarted records the identity of the model server instance behind a
// provider (for example its pod UID and restart count) and clears the partial
// files remembered for it when the identity changed, since Ollama removes them
// when it starts.
func (t *PullTracker) ServerRestarted(provider, instance string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.instances == nil {
		t.instances = map[string]string{}
	}
	previous, seen := t.instances[provider]
	t.instances[provider] = instance
	if !seen || previous == instance {
		return
	}
	for k, l := range t.leftovers {
		if l.provider == provider {
			delete(t.leftovers, k)
		}
	}
}

// pullPhase is where a pull stands after advance.
type pullPhase int

const (
	pullRunning pullPhase = iota
	pullSucceeded
	pullFailed
)

// pullStep is what a controller reports after advancing a pull.
type pullStep struct {
	phase    pullPhase
	progress *aiv1alpha1.PullProgress
	err      error
}

// advance moves the pull for key one step: it reports a pull in progress, how a
// finished one ended, or starts a pull when none is tracked (or the tracked one
// failed for lack of space and the free disk has since changed). owner is the UID of
// the object that asked for the pull; ps.freeBytes is the provider's free disk.
func (t *PullTracker) advance(key string, owner types.UID, ps pullStream) pullStep {
	if state := t.get(key); state != nil {
		state.addOwner(owner)
		if step, handled := t.consume(key, state, ps.freeBytes); handled {
			return step
		}
	}
	startPull(t, key, owner, ps)
	return pullStep{phase: pullRunning, progress: pullProgress(0, 0)}
}

// consume reports a tracked pull. handled is false when the entry was dropped and
// a new pull should start in its place.
func (t *PullTracker) consume(key string, state *pullState, free int64) (step pullStep, handled bool) {
	out := state.outcome()
	switch {
	case !out.finished:
		completed, total := state.totals()
		return pullStep{phase: pullRunning, progress: pullProgress(completed, total)}, true
	case out.err == nil:
		t.forget(key)
		return pullStep{phase: pullSucceeded}, true
	case errors.Is(out.err, errNoSpace) && free != out.freeAtFailure:
		// The provider's free disk changed since the pull failed, so try again.
		t.abandon(key)
		return pullStep{}, false
	case errors.Is(out.err, errNoSpace):
		// Keep the failure until the free disk changes; do not retry on a timer.
		return pullStep{phase: pullFailed, err: out.err}, true
	default:
		t.abandon(key)
		return pullStep{phase: pullFailed, err: out.err}, true
	}
}

// dropUnwanted stops the pulls owner started under a key other than current that
// no object wants any more.
func (t *PullTracker) dropUnwanted(owner types.UID, current string, wanted map[string]bool) {
	for _, key := range t.ownedBy(owner) {
		if key != current && !wanted[key] {
			t.abandon(key)
		}
	}
}

// trackerOrNew returns *slot, creating a tracker there when it is nil.
func trackerOrNew(slot **PullTracker) *PullTracker {
	trackerInit.Lock()
	defer trackerInit.Unlock()
	if *slot == nil {
		*slot = &PullTracker{}
	}
	return *slot
}

var trackerInit sync.Mutex

// tagWantedElsewhere reports whether a Model or EmbeddingModel other than self, not
// being deleted, wants the pull key.
func tagWantedElsewhere(ctx context.Context, c client.Client, self client.Object, key string) (bool, error) {
	wanted, err := wantedPullKeys(ctx, c, self)
	return wanted[key], err
}

// wantedPullKeys lists the provider and tag of every Model and EmbeddingModel not
// being deleted, other than self.
func wantedPullKeys(ctx context.Context, c client.Client, self client.Object) (map[string]bool, error) {
	wanted := map[string]bool{}
	models := &aiv1alpha1.ModelList{}
	if err := c.List(ctx, models); err != nil {
		return nil, err
	}
	for i := range models.Items {
		m := &models.Items[i]
		if m.DeletionTimestamp.IsZero() && !isSameObject(self, m) {
			wanted[pullKey(m.Spec.ProviderRef, m.Spec.Model)] = true
		}
	}
	embeddings := &aiv1alpha1.EmbeddingModelList{}
	if err := c.List(ctx, embeddings); err != nil {
		return nil, err
	}
	for i := range embeddings.Items {
		e := &embeddings.Items[i]
		if e.DeletionTimestamp.IsZero() && !isSameObject(self, e) {
			wanted[pullKey(e.Spec.ProviderRef, e.Spec.Model)] = true
		}
	}
	return wanted, nil
}

// isSameObject reports whether a and b are the same kind with the same namespace and name.
func isSameObject(a, b client.Object) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b) &&
		a.GetNamespace() == b.GetNamespace() && a.GetName() == b.GetName()
}

// releaseStalePulls cancels the pulls self started under a provider or tag other
// than current, unless another Model or EmbeddingModel still needs them.
func releaseStalePulls(ctx context.Context, c client.Client, t *PullTracker, self client.Object, current string) error {
	uid := self.GetUID()
	if len(t.ownedBy(uid)) == 0 {
		return nil
	}
	wanted, err := wantedPullKeys(ctx, c, self)
	if err != nil {
		return err
	}
	t.dropUnwanted(uid, current, wanted)
	return nil
}

// abandonIfUnwanted cancels the pull for key when no Model or EmbeddingModel other
// than self wants it.
func abandonIfUnwanted(ctx context.Context, c client.Client, t *PullTracker, self client.Object, key string) error {
	wanted, err := tagWantedElsewhere(ctx, c, self, key)
	if err != nil {
		return err
	}
	if !wanted {
		t.abandon(key)
	}
	return nil
}

// newPullStream is the pull request for tag onto provider.
func newPullStream(provider *aiv1alpha1.ModelProvider, tag string, httpClient *http.Client) pullStream {
	return pullStream{
		provider: provider.Name, endpoint: provider.Spec.Endpoint, tag: tag,
		freeBytes: providerFreeBytes(provider), client: httpClient,
	}
}

// pullStream is the pull request, the stream settings and what ends the stream.
type pullStream struct {
	provider  string // ModelProvider name, to attribute the disk the pull uses
	endpoint  string
	tag       string
	freeBytes int64 // provider free disk when known, otherwise 0
	client    *http.Client
}

// startPull begins a streaming pull in a goroutine and returns at once. The check
// for a pull already tracked under key and the insert happen under one lock, so
// concurrent reconciles of Models sharing a tag start one pull. owner is the UID
// of the Model that asked for it.
func startPull(tracker *PullTracker, key string, owner types.UID, ps pullStream) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.pulls[key] != nil {
		return
	}
	if tracker.pulls == nil {
		tracker.pulls = map[string]*pullState{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := &pullState{cancel: cancel, owners: map[types.UID]bool{owner: true}, provider: ps.provider, layers: map[string]*pullLayer{}}
	tracker.pulls[key] = state
	delete(tracker.leftovers, key) // the resumed pull counts the partial files itself
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
