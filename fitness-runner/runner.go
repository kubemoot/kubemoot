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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
	"github.com/kubemoot/kubemoot/operator/pkg/sse"
)

// SignalEvent and AssertionResult are the shared assertion engine's types: the
// runner records them in the transcript and the Job annotation.
type (
	SignalEvent     = fitnessscript.SignalEvent
	AssertionResult = fitnessscript.AssertionResult
)

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

// answeringThreadID returns the thread the stream last announced. The gateway announces
// a second thread when the coordinator restarted and took the request again; the
// earlier thread was abandoned and the later one answers.
func answeringThreadID(events []SignalEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].ThreadID != "" {
			return events[i].ThreadID
		}
	}
	return ""
}

// discussionStartResponse is the JSON body returned by POST to the discussion endpoint.
type discussionStartResponse struct {
	ConversationID string `json:"conversationId"`
}

// newHTTPClient returns the client for the discussion gateway. The operator points
// the runner at the gateway's in-cluster Service over plain HTTP; an https endpoint
// gets Go's standard certificate verification.
func newHTTPClient() *http.Client {
	return &http.Client{
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
			_ = resp.Body.Close()
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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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

// RunFitnessTest executes the full fitness test lifecycle:
//  1. POST question to endpoint
//  2. Collect SSE events until done or timeout
//  3. Evaluate each assertion
//
// Returns the list of AssertionResult values.
func RunFitnessTest(ctx context.Context, ft fitnessscript.FitnessTest, endpoint string, maxDuration time.Duration) *RunOutcome {
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
	synthesis := fitnessscript.FindSynthesis(events)
	results := fitnessscript.Evaluate(ft.Assertions, fitnessscript.RunState{
		PostOK: postOK, Events: events, Synthesis: synthesis, TimedOut: timedOut,
	})
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
	answered := postOK && fitnessscript.HasDone(events)

	return &RunOutcome{
		Assertions:     results,
		Events:         events,
		ConversationID: conversationID,
		ThreadID:       answeringThreadID(events),
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
		if fitnessscript.HasDone(collected) || len(collected) > len(events) {
			events = collected // replay returns the full timeline; keep the richest
		}
		if streamCtx.Err() == context.DeadlineExceeded {
			return events, true
		}
		if fitnessscript.HasDone(events) {
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

// labelUnjudged rewrites the message of each deferred assertion in a run that is
// not part of a CrewFitnessSuite. Only a suite runs the judge, so a standalone run's
// quality is never scored, and the message says so instead of reading as a pass.
func labelUnjudged(assertions []fitnessscript.Assertion, results []AssertionResult) {
	for i := range results {
		if i < len(assertions) && assertions[i].Kind == fitnessscript.KindDeferred {
			results[i].Message = fmt.Sprintf("DEFER %s - not scored: quality is judged only when "+
				"this scenario runs in a CrewFitnessSuite", assertions[i].Keyword)
		}
	}
}
