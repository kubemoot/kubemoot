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

package embedding

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestEmbedNormalizesVector(t *testing.T) {
	srv := serve(200, `{"data":[{"embedding":[3,4]}]}`)
	defer srv.Close()
	v, err := Embed(context.Background(), srv.Client(), srv.URL+"/", "m", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("want [0.6 0.8], got %v", v)
	}
}

func TestEmbedRefusedModelIsNotEmbedding(t *testing.T) {
	for _, code := range []int{400, 404, 422, 501} {
		srv := serve(code, `{"error":"Model does not support Embeddings API"}`)
		_, err := Embed(context.Background(), srv.Client(), srv.URL, "chat", "hi")
		srv.Close()
		if !errors.Is(err, ErrNotEmbedding) {
			t.Errorf("status %d: want ErrNotEmbedding, got %v", code, err)
		}
	}
}

func TestEmbedServerTroubleIsNotAVerdict(t *testing.T) {
	for _, code := range []int{408, 429, 500, 503} {
		srv := serve(code, `{}`)
		_, err := Embed(context.Background(), srv.Client(), srv.URL, "m", "hi")
		srv.Close()
		if err == nil || errors.Is(err, ErrNotEmbedding) {
			t.Errorf("status %d: want a plain error, got %v", code, err)
		}
	}
}

func TestEmbedBadAnswers(t *testing.T) {
	for _, body := range []string{`not json`, `{"data":[]}`, `{"data":[{"embedding":[]}]}`} {
		srv := serve(200, body)
		_, err := Embed(context.Background(), srv.Client(), srv.URL, "m", "hi")
		srv.Close()
		if err == nil {
			t.Errorf("body %q: want an error", body)
		}
	}
}

func TestEmbedUnreachable(t *testing.T) {
	srv := serve(200, `{}`)
	url := srv.URL
	srv.Close()
	if _, err := Embed(context.Background(), http.DefaultClient, url, "m", "hi"); err == nil || errors.Is(err, ErrNotEmbedding) {
		t.Fatalf("want a plain error, got %v", err)
	}
}

func TestNormalizeZeroVectorUnchanged(t *testing.T) {
	v := Normalize([]float32{0, 0, 0})
	if v[0] != 0 || v[1] != 0 || v[2] != 0 {
		t.Fatalf("zero vector changed: %v", v)
	}
}
