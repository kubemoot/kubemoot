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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPGetSendsGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	resp, err := httpGet(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("httpGet: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestHTTPGetStopsOnCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if resp, err := httpGet(ctx, srv.Client(), srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected an error for a cancelled context")
	}
}

func TestHTTPGetRejectsBadURL(t *testing.T) {
	if _, err := httpGet(context.Background(), http.DefaultClient, "://no-scheme"); err == nil {
		t.Fatal("expected an error for an unparsable URL")
	}
}

func TestRequeueNowRequeuesWithoutWaiting(t *testing.T) {
	res := requeueNow()
	if res.RequeueAfter <= 0 || res.RequeueAfter > immediateRequeueDelay {
		t.Errorf("RequeueAfter = %v, want (0, %v]", res.RequeueAfter, immediateRequeueDelay)
	}
}
