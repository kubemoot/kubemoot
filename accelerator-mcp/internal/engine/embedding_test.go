package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEngine_embeddingOnlyFollowsTheCapabilities(t *testing.T) {
	cases := []struct {
		name string
		caps string
		want bool
	}{
		{"embedding only", `["embedding"]`, true},
		{"chat", `["completion","tools"]`, false},
		{"both", `["completion","embedding"]`, false},
		{"none listed", `[]`, false},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/show" {
				t.Errorf("%s: asked %s", c.name, r.URL.Path)
			}
			_, _ = io.WriteString(w, `{"capabilities":`+c.caps+`}`)
		}))
		got, err := OllamaEngine{Endpoint: srv.URL}.EmbeddingOnly(context.Background(), testModel)
		srv.Close()
		check(t, c.name, err == nil && got == c.want, got)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer failing.Close()
	_, err := OllamaEngine{Endpoint: failing.URL}.EmbeddingOnly(context.Background(), testModel)
	check(t, "an unknown model is an error", err != nil, err)
}
