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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

func writeToken(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An answering gateway that records the Authorization header of every call.
func recordingGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"conversationId":"c1"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"synthesis\",\"content\":\"a\"}\ndata: {\"type\":\"done\"}\n"))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestRunnerSendsItsTokenOnEveryGatewayCall(t *testing.T) {
	t.Setenv(envTokenFile, writeToken(t, "the-token\n"))
	srv, seen := recordingGateway(t)

	ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
	out := RunFitnessTest(context.Background(), ft, srv.URL, 10*time.Second)

	if !out.Answered {
		t.Fatal("the run was not answered")
	}
	calls := seen()
	if len(calls) < 2 {
		t.Fatalf("expected the POST and the stream, saw %d calls", len(calls))
	}
	for i, got := range calls {
		if got != "Bearer the-token" {
			t.Errorf("call %d Authorization = %q, want the bearer token without its newline", i, got)
		}
	}
}

func TestRunnerReadsARenewedTokenOnTheNextCall(t *testing.T) {
	path := writeToken(t, "old")
	t.Setenv(envTokenFile, path)
	srv, seen := recordingGateway(t)
	client := newHTTPClient()

	get := func() {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	get()
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	get()
	if calls := seen(); calls[0] != "Bearer old" || calls[1] != "Bearer new" {
		t.Errorf("calls = %v, want the renewed token on the second", calls)
	}
}

func TestRunnerSendsNoAuthorizationWithoutATokenFile(t *testing.T) {
	t.Setenv(envTokenFile, "")
	srv, seen := recordingGateway(t)
	resp, err := newHTTPClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := seen()[0]; got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestRunnerFailsWhenItsTokenFileIsMissing(t *testing.T) {
	t.Setenv(envTokenFile, filepath.Join(t.TempDir(), "absent"))
	srv, _ := recordingGateway(t)
	if _, err := newHTTPClient().Get(srv.URL); err == nil {
		t.Fatal("expected an error naming the unreadable token")
	}
}

func TestRunFitnessTestNamesARefusal(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized: "the crew refused the fitness runner: it requires identity (401)",
		http.StatusForbidden:    "the crew refused the fitness runner: the runner's ServiceAccount may not use the crew (403)",
	}
	for status, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
		out := RunFitnessTest(context.Background(), ft, srv.URL, 3*time.Second)
		srv.Close()

		if out.Refusal != want {
			t.Errorf("status %d: Refusal = %q, want %q", status, out.Refusal, want)
		}
		if out.Answered {
			t.Errorf("status %d: a refused run is not answered", status)
		}
	}
}

func TestRunFitnessTestDoesNotCallAServerErrorARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ft := fitnessscript.FitnessTest{Constants: map[string]string{constQuestion: "q"}}
	if out := RunFitnessTest(context.Background(), ft, srv.URL, 3*time.Second); out.Refusal != "" {
		t.Errorf("Refusal = %q for a 500, want none (it is retried)", out.Refusal)
	}
}
