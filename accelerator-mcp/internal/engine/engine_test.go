package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testModel = "qwen3:14b"

func check(t *testing.T, what string, ok bool, got any) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: %+v", what, got)
	}
}

func TestOllamaEngine_readsItsTimingsAndResidents(t *testing.T) {
	var unloaded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:14b","size":10485760000,"size_vram":9437184000,"context_length":8192}]}`))
		case strings.Contains(string(body), `"keep_alive":0`):
			unloaded = string(body)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"load_duration":2500000000,"prompt_eval_count":900,"prompt_eval_duration":600000000,` +
				`"eval_count":48,"eval_duration":1200000000,"total_duration":4400000000}`))
		}
	}))
	defer srv.Close()
	engine := OllamaEngine{Endpoint: srv.URL}

	reply, err := engine.Generate(context.Background(), testModel, "hi", 48)
	check(t, "generate", err == nil && reply == Reply{LoadMs: 2500, PromptTokens: 900, PromptMs: 600,
		OutputTokens: 48, GenerateMs: 1200, TotalMs: 4400}, reply)
	residents, err := engine.Loaded(context.Background())
	check(t, "loaded", err == nil && len(residents) == 1 &&
		residents[0] == Resident{Model: testModel, SizeMiB: 10_000, VRAMMiB: 9_000, ContextLength: 8192}, residents)
	err = engine.Unload(context.Background(), testModel)
	check(t, "unload", err == nil && strings.Contains(unloaded, testModel), unloaded)
}

func TestOllamaEngine_anErrorStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	engine := OllamaEngine{Endpoint: srv.URL}
	if _, err := engine.Generate(context.Background(), testModel, "hi", 8); err == nil {
		t.Fatal("generate should fail on a 500")
	}
	if _, err := engine.Loaded(context.Background()); err == nil {
		t.Fatal("loaded should fail on a 500")
	}
}
