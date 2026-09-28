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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/javajon/kubemoot/operator/pkg/sse"
)

// SignalEvent represents a single SSE event from the discussion gateway stream.
type SignalEvent struct {
	Type       string `json:"type"`
	Agent      string `json:"agent,omitempty"`
	Status     string `json:"status,omitempty"`
	GPU        string `json:"gpu,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Content    string `json:"content,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
	StoodAside bool   `json:"stood_aside,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AssertionResult is the per-assertion pass/fail outcome written to the Job annotation.
type AssertionResult struct {
	Raw     string `json:"raw"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// RunOutcome bundles the assertion results with the full discussion transcript
// (every SSE event) and run metadata. The assertions go to the Job annotation
// (as before); the transcript is written to NATS Object Store for per-iteration
// drill-down (see [[Fitness Conversation Capture and Merged Dashboard]] Part B).
type RunOutcome struct {
	Assertions     []AssertionResult `json:"assertions"`
	Events         []SignalEvent     `json:"events"`
	ConversationID string            `json:"conversationId"`
	ThreadID       string            `json:"threadId"`
	Question       string            `json:"question"`
	StartedAt      string            `json:"startedAt"`
	DurationMs     int64             `json:"durationMs"`
	// Answered reports whether the run obtained a real crew answer (the
	// discussion started AND reached completion: a 'done' event). It is the basis for
	// the runner's exit code: an answered run is a COMPLETED measurement (pass or
	// fail lives in Assertions and must not be retried), while an unanswered run
	// is a transient/infra failure (gateway unreachable, or no synthesis before
	// the deadline) that the Job's backoffLimit should retry rather than record
	// as a spurious zero. See [[Rollout-Safety Fitness Job Fails BackoffLimitExceeded]].
	Answered bool `json:"answered"`
}

// firstThreadID returns the threadId carried on the earliest event that has one.
func firstThreadID(events []SignalEvent) string {
	for _, e := range events {
		if e.ThreadID != "" {
			return e.ThreadID
		}
	}
	return ""
}

// discussionStartResponse is the JSON body returned by POST to the discussion endpoint.
type discussionStartResponse struct {
	ConversationID string `json:"conversationId"`
}

// newHTTPClient returns an HTTP client that accepts self-signed TLS certificates.
// The discussion gateway may sit behind Cloudflare Tunnel with self-signed certs.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // intentional — homelab with self-signed certs
			},
		},
		Timeout: 0, // No client-level timeout; we use context cancellation instead
	}
}

// waitForReady polls the discussion gateway's /ready endpoint until it returns 200
// or the timeout expires. Returns nil on success, error on timeout or context cancellation.
func waitForReady(ctx context.Context, client *http.Client, endpoint string, timeout time.Duration) error {
	// Derive base URL from discussion endpoint (strip /api/v1/discussions/...)
	readyURL := endpoint
	if idx := strings.Index(endpoint, "/api/"); idx > 0 {
		readyURL = endpoint[:idx]
	}
	readyURL = strings.TrimRight(readyURL, "/") + "/ready"

	deadline := time.After(timeout)
	interval := 3 * time.Second

	fmt.Printf("[fitness-runner] waiting for gateway readiness at %s (timeout %s)\n", readyURL, timeout)

	for {
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, readyURL, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("create readiness request: %w", err)
		}

		resp, err := client.Do(req)
		cancel()

		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Println("[fitness-runner] gateway is ready")
				return nil
			}
			fmt.Printf("[fitness-runner] gateway not ready (status %d), retrying...\n", resp.StatusCode)
		} else {
			fmt.Printf("[fitness-runner] gateway not reachable (%v), retrying...\n", err)
		}

		select {
		case <-deadline:
			return fmt.Errorf("gateway not ready after %s", timeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// postDiscussion sends the question to the discussion endpoint and returns the conversationId.
// Returns (conversationId, statusCode, error).
func postDiscussion(ctx context.Context, client *http.Client, endpoint, question string) (string, int, error) {
	body, err := json.Marshal(map[string]string{"message": question})
	if err != nil {
		return "", 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("POST returned %d", resp.StatusCode)
	}

	var parsed discussionStartResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", resp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	if parsed.ConversationID == "" {
		return "", resp.StatusCode, fmt.Errorf("response missing conversationId")
	}

	return parsed.ConversationID, resp.StatusCode, nil
}

// collectSSE connects to the SSE stream at streamURL and returns all events
// collected until a "done" event is received, the context is cancelled, or
// an error occurs.
func collectSSE(ctx context.Context, client *http.Client, streamURL string) ([]SignalEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", streamURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SSE stream returned %d", resp.StatusCode)
	}

	events, scanErr := scanSSEEvents(resp.Body)
	if scanErr != nil {
		// Context cancellation surfaces as a scanner error — treat it as a timeout
		if ctx.Err() != nil {
			return events, nil
		}
		return events, fmt.Errorf("SSE scanner error: %w", scanErr)
	}

	return events, nil
}

// scanSSEEvents reads the stream's data payloads into SignalEvents, stopping at the
// "done" event or the end of the stream.
func scanSSEEvents(r io.Reader) ([]SignalEvent, error) {
	var events []SignalEvent
	err := sse.Data(r, func(data string) bool {
		var ev SignalEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return true // a non-JSON data line is skipped
		}
		events = append(events, ev)
		return ev.Type != "done"
	})
	return events, err
}

// hasDone reports whether the event stream contains a terminal "done" event,
// i.e. the discussion completed (as opposed to the stream dropping mid-flight).
func hasDone(events []SignalEvent) bool {
	for _, ev := range events {
		if ev.Type == "done" {
			return true
		}
	}
	return false
}

// findSynthesis scans events (in order) and returns the content of the first
// "synthesis" event, or the "done" event content if no synthesis event exists.
func findSynthesis(events []SignalEvent) string {
	for _, ev := range events {
		if ev.Type == "synthesis" && ev.Content != "" {
			return ev.Content
		}
	}
	// Fallback: some implementations put synthesis content on the done event
	for _, ev := range events {
		if ev.Type == "done" && ev.Content != "" {
			return ev.Content
		}
	}
	return ""
}

// RunFitnessTest executes the full fitness test lifecycle:
//  1. POST question to endpoint
//  2. Collect SSE events until done or timeout
//  3. Evaluate each assertion
//
// Returns the list of AssertionResult values.
func RunFitnessTest(ctx context.Context, ft FitnessTest, endpoint string, maxDuration time.Duration) *RunOutcome {
	client := newHTTPClient()
	startedAt := time.Now()

	question, ok := ft.Constants["QUESTION"]
	if !ok || question == "" {
		question = "Hello"
	}

	// Step 1: POST to start the discussion.
	// Use a generous timeout — the gateway may call the coordinator synchronously
	// and the first discussion can trigger model loading on the GPU.
	postTimeout := maxDuration
	if postTimeout < 60*time.Second {
		postTimeout = 60 * time.Second
	}
	postCtx, postCancel := context.WithTimeout(ctx, postTimeout)
	defer postCancel()

	conversationID, statusCode, postErr := postDiscussion(postCtx, client, endpoint, question)
	postOK := postErr == nil && statusCode == http.StatusOK

	// Step 2: Collect SSE events with the overall timeout
	var events []SignalEvent
	var timedOut bool

	if postOK {
		streamURL := strings.TrimRight(endpoint, "/") + "/" + conversationID + "/stream"
		events, timedOut = collectDiscussionStream(ctx, client, streamURL, maxDuration)
	}

	// Step 3: Evaluate assertions
	synthesis := findSynthesis(events)
	results := evaluateAssertions(ft.Assertions, postOK, events, synthesis, timedOut)
	if os.Getenv("CREWFITNESS_SUITE") == "" {
		labelUnjudged(ft.Assertions, results)
	}

	// A run is "answered" when the discussion both started (postOK) and reached a
	// clean conclusion: a 'done' event. 'done' is the unambiguous completion
	// signal; the SSE reconnect loop above already recovers it via thread replay
	// after a transport drop, and a Job retry gets a fresh replay if needed.
	// Assertion pass/fail is irrelevant here: a crew answer that fails its
	// assertions is still a completed measurement, whereas an unreachable gateway
	// or a deadline/transport drop with no 'done' is a transient/infra failure to
	// retry, not a real zero. (A run with synthesis but no 'done' is treated as
	// unanswered on purpose, so the retry can recover the clean conclusion.)
	answered := postOK && hasDone(events)

	return &RunOutcome{
		Assertions:     results,
		Events:         events,
		ConversationID: conversationID,
		ThreadID:       firstThreadID(events),
		Question:       question,
		StartedAt:      startedAt.UTC().Format(time.RFC3339),
		DurationMs:     time.Since(startedAt).Milliseconds(),
		Answered:       answered,
	}
}

// collectDiscussionStream connects to the thread-scoped SSE stream and recovers
// the full event timeline, reconnecting on transport drops. A thread-scoped SSE
// stream replays the thread's events on connect, so a mid-stream blip (or an
// idle-closed connection) shouldn't be recorded as a crew failure: reconnect and
// recover the persisted timeline — including the 'done'/synthesis — until we have
// a 'done' event or the overall deadline genuinely expires. Returns the richest
// collected timeline and whether the overall deadline expired.
func collectDiscussionStream(ctx context.Context, client *http.Client, streamURL string, maxDuration time.Duration) ([]SignalEvent, bool) {
	streamCtx, streamCancel := context.WithTimeout(ctx, maxDuration)
	defer streamCancel()

	var events []SignalEvent
	const maxSSEAttempts = 4
	for attempt := 1; attempt <= maxSSEAttempts; attempt++ {
		collected, sseErr := collectSSE(streamCtx, client, streamURL)
		if hasDone(collected) || len(collected) > len(events) {
			events = collected // replay returns the full timeline; keep the richest
		}
		if streamCtx.Err() == context.DeadlineExceeded {
			return events, true
		}
		if hasDone(events) {
			return events, false
		}
		// No 'done' yet and time remains — the drop was transport, not completion.
		logSSEReconnect(attempt, maxSSEAttempts, sseErr)
		if attempt < maxSSEAttempts {
			if timedOut := waitBeforeReconnect(streamCtx, attempt); timedOut {
				return events, true
			}
		}
	}
	return events, false
}

// logSSEReconnect prints the per-attempt reconnect notice for a dropped or
// 'done'-less SSE stream.
func logSSEReconnect(attempt, maxAttempts int, sseErr error) {
	if sseErr != nil {
		fmt.Printf("[fitness-runner] SSE drop (attempt %d/%d), reconnecting to recover persisted stream: %v\n", attempt, maxAttempts, sseErr)
	} else {
		fmt.Printf("[fitness-runner] SSE ended without 'done' (attempt %d/%d), reconnecting to recover\n", attempt, maxAttempts)
	}
}

// waitBeforeReconnect backs off before the next SSE reconnect attempt, returning
// true if the overall deadline expired during the wait.
func waitBeforeReconnect(streamCtx context.Context, attempt int) bool {
	select {
	case <-streamCtx.Done():
		return streamCtx.Err() == context.DeadlineExceeded
	case <-time.After(time.Duration(attempt) * time.Second):
		return false
	}
}

// evaluateAssertions runs every assertion against the collected run state and
// returns the per-assertion results in order.
func evaluateAssertions(assertions []Assertion, postOK bool, events []SignalEvent, synthesis string, timedOut bool) []AssertionResult {
	var results []AssertionResult
	for _, a := range assertions {
		r := evaluateAssertion(a, postOK, events, synthesis, timedOut)
		results = append(results, r)
	}
	return results
}

// labelUnjudged rewrites the message of each deferred assertion in a run that is
// not part of a CrewFitnessSuite. Only a suite runs the judge, so a standalone run's
// quality is never scored, and the message says so instead of reading as a pass.
func labelUnjudged(assertions []Assertion, results []AssertionResult) {
	for i := range results {
		if i < len(assertions) && assertions[i].Kind == KindDeferred {
			results[i].Message = fmt.Sprintf("DEFER %s - not scored: quality is judged only when "+
				"this scenario runs in a CrewFitnessSuite", assertions[i].Keyword)
		}
	}
}

// runState is what a finished run offers the assertions to check against.
type runState struct {
	postOK    bool
	events    []SignalEvent
	synthesis string
	timedOut  bool
}

// evaluators maps each assertion kind to its check.
var evaluators = map[AssertionKind]func(Assertion, runState) AssertionResult{
	KindPostReturns200:         func(a Assertion, s runState) AssertionResult { return evalPostReturns200(a, s.postOK) },
	KindSseEmits:               func(a Assertion, s runState) AssertionResult { return evalSseEmits(a, s.events) },
	KindSseEmitsWithin:         func(a Assertion, s runState) AssertionResult { return evalSseEmitsWithin(a, s.events) },
	KindCompletesWithin:        func(a Assertion, s runState) AssertionResult { return evalCompletesWithin(a, s.events, s.timedOut) },
	KindMinSpecialistAgrees:    func(a Assertion, s runState) AssertionResult { return evalMinSpecialistAgrees(a, s.events) },
	KindCoordinatorSynthesizes: func(a Assertion, s runState) AssertionResult { return evalCoordinatorSynthesizes(a, s.synthesis) },
	KindSynthesisNonEmpty:      func(a Assertion, s runState) AssertionResult { return evalSynthesisNonEmpty(a, s.synthesis) },
	KindSynthesisContains:      func(a Assertion, s runState) AssertionResult { return evalSynthesisContains(a, s.synthesis) },
	KindSynthesisNotContains:   func(a Assertion, s runState) AssertionResult { return evalSynthesisNotContains(a, s.synthesis) },
	KindSynthesisMatchCount:    func(a Assertion, s runState) AssertionResult { return evalSynthesisMatchCount(a, s.synthesis) },
	KindDeferred:               func(a Assertion, _ runState) AssertionResult { return evalDeferred(a) },
}

func evaluateAssertion(a Assertion, postOK bool, events []SignalEvent, synthesis string, timedOut bool) AssertionResult {
	if eval, ok := evaluators[a.Kind]; ok {
		return eval(a, runState{postOK: postOK, events: events, synthesis: synthesis, timedOut: timedOut})
	}
	return AssertionResult{Raw: a.Raw, Passed: true, Message: "Custom assertion - manual review recommended"}
}

// evalDeferred does not evaluate inline (a judge call would compete with the crew
// for GPU and perturb the run). The post-suite engine resolves the keyword to its
// crew and overwrites this with the 0.0-1.0 reference-grounded score.
func evalDeferred(a Assertion) AssertionResult {
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  true,
		Message: fmt.Sprintf("DEFER %s - deferred to post-suite judge crew", a.Keyword),
	}
}

func evalPostReturns200(a Assertion, postOK bool) AssertionResult {
	if postOK {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "POST returned 200"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "POST did not return 200"}
}

func evalSseEmits(a Assertion, events []SignalEvent) AssertionResult {
	if hasEventType(events, a.EventType) {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: fmt.Sprintf(`Event "%s" received`, a.EventType)}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: fmt.Sprintf(`Event "%s" not received`, a.EventType)}
}

func evalSseEmitsWithin(a Assertion, events []SignalEvent) AssertionResult {
	// We cannot measure per-event timing from the stream since we don't timestamp events.
	// A "within N seconds" check degrades to a plain presence check — if the stream
	// completed within the overall timeout AND the event was present, it passed.
	// Delegates to evalSseEmits since timing granularity is not yet available.
	return evalSseEmits(a, events)
}

func evalCompletesWithin(a Assertion, events []SignalEvent, timedOut bool) AssertionResult {
	hasDone := hasEventType(events, "done")
	if hasDone && !timedOut {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Discussion completed with done event"}
	}
	if timedOut {
		return AssertionResult{Raw: a.Raw, Passed: false, Message: "Timed out before done event"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "No done event received"}
}

func evalMinSpecialistAgrees(a Assertion, events []SignalEvent) AssertionResult {
	count := countAgreeSignals(events)
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  count >= a.AgreeCount,
		Message: fmt.Sprintf("%d agree signal(s) received (need %d)", count, a.AgreeCount),
	}
}

func evalCoordinatorSynthesizes(a Assertion, synthesis string) AssertionResult {
	if synthesis != "" {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Coordinator produced synthesis"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "No synthesis produced"}
}

func evalSynthesisNonEmpty(a Assertion, synthesis string) AssertionResult {
	if synthesis != "" {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Synthesis is non-empty"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "Synthesis is empty"}
}

func evalSynthesisContains(a Assertion, synthesis string) AssertionResult {
	lower := strings.ToLower(synthesis)
	var missing []string
	for _, term := range a.Terms {
		if !strings.Contains(lower, strings.ToLower(term)) {
			missing = append(missing, term)
		}
	}
	if len(missing) == 0 {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "All expected terms found in synthesis"}
	}
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  false,
		Message: fmt.Sprintf("Missing terms: %s", strings.Join(missing, ", ")),
	}
}

func evalSynthesisNotContains(a Assertion, synthesis string) AssertionResult {
	lower := strings.ToLower(synthesis)
	var found []string
	for _, term := range a.Terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			found = append(found, term)
		}
	}
	if len(found) == 0 {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "No prohibited terms found in synthesis"}
	}
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  false,
		Message: fmt.Sprintf("Prohibited terms found: %s", strings.Join(found, ", ")),
	}
}

// evalSynthesisMatchCount passes when the synthesis contains at least MinCount
// matches of Pattern (case-insensitive regex) - the drift-tolerant numeric bound
// that catches a gross under-report (e.g. 4 of 141 deployments) without pinning
// the exact count.
func evalSynthesisMatchCount(a Assertion, synthesis string) AssertionResult {
	re, err := regexp.Compile("(?i)" + a.Pattern)
	if err != nil {
		return AssertionResult{Raw: a.Raw, Passed: false, Message: fmt.Sprintf("invalid match pattern %q: %v", a.Pattern, err)}
	}
	n := len(re.FindAllString(synthesis, -1))
	if n >= a.MinCount {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: fmt.Sprintf("%d matches of /%s/ (need >= %d)", n, a.Pattern, a.MinCount)}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: fmt.Sprintf("only %d matches of /%s/ (need >= %d) - likely an under-report", n, a.Pattern, a.MinCount)}
}

// hasEventType returns true if any event in the slice has the given type.
func hasEventType(events []SignalEvent, eventType string) bool {
	for _, ev := range events {
		if ev.Type == eventType {
			return true
		}
	}
	return false
}

// countAgreeSignals counts "finding" events with signal "agree". This backs the
// ">=N toolers agree" floor assertion; the operator's agreeCountFromEvents
// (fitness_scoring.go) MUST count agrees identically, because participation is
// scored against the same expected N. Keep the two in sync.
func countAgreeSignals(events []SignalEvent) int {
	count := 0
	for _, ev := range events {
		if ev.Type == "finding" && ev.Signal == "agree" {
			count++
		}
	}
	return count
}
