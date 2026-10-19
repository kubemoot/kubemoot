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

package vllm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serveModelsPage(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServedModelsReadsTheNameAndWhereTheWeightsCameFrom(t *testing.T) {
	srv := serveModelsPage(t, http.StatusOK, `{"data":[{"id":"qwen3:14b","root":"Qwen/Qwen3-14B-AWQ"},{"id":"alias"}]}`)
	got, err := New(srv.Client()).ServedModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (ServedModel{ID: "qwen3:14b", Root: "Qwen/Qwen3-14B-AWQ"}) || got[1] != (ServedModel{ID: "alias"}) {
		t.Errorf("served models = %+v", got)
	}
}

func TestServedModelsFailsOnAnErrorOrABadPage(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"a server error": {http.StatusServiceUnavailable, ""},
		"not found":      {http.StatusNotFound, ""},
		"not JSON":       {http.StatusOK, "<html>"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := serveModelsPage(t, tc.status, tc.body)
			if got, err := New(srv.Client()).ServedModels(context.Background(), srv.URL); err == nil {
				t.Errorf("served models = %+v, want an error", got)
			}
		})
	}
}

func TestServedModelsOfAnEmptyListIsEmptyNotAnError(t *testing.T) {
	srv := serveModelsPage(t, http.StatusOK, `{"data":[]}`)
	got, err := New(srv.Client()).ServedModels(context.Background(), srv.URL)
	if err != nil || len(got) != 0 {
		t.Errorf("served models %+v err %v", got, err)
	}
}
