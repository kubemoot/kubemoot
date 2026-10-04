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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

func TestWaitForReady_ImmediateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{}
	endpoint := srv.URL + "/api/v1/discussions/test-crew"

	err := waitForReady(context.Background(), client, endpoint, 10*time.Second)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestWaitForReady_BecomesReadyAfterRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ready" {
			n := calls.Add(1)
			if n >= 3 {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{}
	endpoint := srv.URL + "/api/v1/discussions/test-crew"

	err := waitForReady(context.Background(), client, endpoint, 30*time.Second)
	if err != nil {
		t.Errorf("expected success after retries, got: %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls.Load())
	}
}

func TestWaitForReady_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := &http.Client{}
	endpoint := srv.URL + "/api/v1/discussions/test-crew"

	err := waitForReady(context.Background(), client, endpoint, 5*time.Second)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestWaitForReady_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := &http.Client{}
	endpoint := srv.URL + "/api/v1/discussions/test-crew"

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()

	err := waitForReady(ctx, client, endpoint, 30*time.Second)
	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
}

// TestRunFitnessTest_RecoversAfterSSEDrop verifies that a transport drop before
// the 'done' event is not recorded as a crew failure: the runner reconnects to
// the (replaying) stream and recovers the synthesis + done.
func TestRunFitnessTest_RecoversAfterSSEDrop(t *testing.T) {
	var connects int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversationId":"conv-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		n := atomic.AddInt32(&connects, 1)
		if n == 1 {
			// First connect: partial stream, drop before 'done'.
			_, _ = w.Write([]byte("data: {\"type\":\"thread_found\"}\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"agree\"}\n"))
			return
		}
		// Reconnect: full replay including synthesis + done.
		_, _ = w.Write([]byte("data: {\"type\":\"thread_found\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"agree\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"The answer is 42.\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n"))
	}))
	defer srv.Close()

	ft := fitnessscript.FitnessTest{
		Constants:  map[string]string{constQuestion: "q"},
		Assertions: []fitnessscript.Assertion{{Raw: assertSynthesisNonEmpty, Kind: fitnessscript.KindSynthesisNonEmpty}},
	}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 20*time.Second)

	if !fitnessscript.HasDone(out.Events) {
		t.Errorf("expected 'done' recovered after reconnect; got %d events", len(out.Events))
	}
	if fitnessscript.FindSynthesis(out.Events) == "" {
		t.Error("expected synthesis recovered after reconnect")
	}
	if c := atomic.LoadInt32(&connects); c < 2 {
		t.Errorf("expected >=2 stream connects (reconnect), got %d", c)
	}
	if !out.Assertions[0].Passed {
		t.Error("expected synthesis-non-empty assertion to pass after recovery")
	}
	if !out.Answered {
		t.Error("expected Answered=true after recovering a synthesis+done stream")
	}
}

// A run that reaches the crew and receives a synthesized, completed answer is
// "answered" regardless of assertion outcome; it is a real measurement and must
// not be retried by the Job. See [[Rollout-Safety Fitness Job Fails BackoffLimitExceeded]].
func TestRunFitnessTest_AnsweredOnCompletedDiscussion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversationId":"conv-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"answer\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n"))
	}))
	defer srv.Close()

	ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 10*time.Second)

	if !out.Answered {
		t.Error("expected Answered=true for a completed discussion")
	}
}

// A run that cannot start the discussion (gateway error) is a transient/infra
// failure, NOT a measurement: Answered must be false so the runner exits non-zero
// and the Job retries instead of recording a spurious zero.
func TestRunFitnessTest_NotAnsweredWhenGatewayFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 3*time.Second)

	if out.Answered {
		t.Error("expected Answered=false when the gateway returns an error on POST")
	}
}

// The central invariant of the fix: a real crew answer that FAILS its assertions
// is still Answered (exit 0, recorded as Failed), and must NOT be retried.
func TestRunFitnessTest_AnsweredEvenWhenAssertionFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversationId":"conv-1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"the answer is blue\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"done\"}\n"))
	}))
	defer srv.Close()

	ft := fitnessscript.FitnessTest{
		Constants:  map[string]string{constQuestion: "q"},
		Assertions: []fitnessscript.Assertion{{Raw: "synthesis contains green", Kind: fitnessscript.KindSynthesisContains, Terms: []string{"green"}}},
	}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 10*time.Second)

	if !out.Answered {
		t.Error("expected Answered=true: the crew answered even though the assertion fails")
	}
	if out.Assertions[0].Passed {
		t.Error("expected the synthesis-contains assertion to FAIL (answer was 'blue', not 'green')")
	}
}

// A discussion that emits a synthesis but never a clean 'done' is treated as
// unanswered on purpose: the retry recovers the clean conclusion rather than
// recording a possibly-truncated stream as a final measurement.
func TestRunFitnessTest_NotAnsweredWhenNoDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"conversationId":"conv-1"}`))
			return
		}
		// Synthesis arrives, but the stream drops before 'done' on every connect.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"partial answer\"}\n"))
	}))
	defer srv.Close()

	ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 2*time.Second)

	if fitnessscript.FindSynthesis(out.Events) == "" {
		t.Fatal("test precondition: expected a synthesis event to have been captured")
	}
	if out.Answered {
		t.Error("expected Answered=false when 'done' never arrives, even though synthesis was seen")
	}
}

// The gateway client verifies certificates: a server with a certificate no
// trusted CA signed is refused, while the in-cluster plain-HTTP gateway works.
func TestNewHTTPClientVerifiesCertificates(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	untrusted := httptest.NewTLSServer(ok)
	defer untrusted.Close()
	resp, err := newHTTPClient().Get(untrusted.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected a self-signed certificate to be rejected")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("expected a certificate error, got %v", err)
	}

	plain := httptest.NewServer(ok)
	defer plain.Close()
	resp, err = newHTTPClient().Get(plain.URL)
	if err != nil {
		t.Fatalf("plain HTTP gateway: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("plain HTTP gateway status = %d, want 200", resp.StatusCode)
	}
}
