package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// showServer answers POST /api/show with status and body, and records the request body.
func showServer(t *testing.T, status int, body string, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if got != nil {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const qwen3Show = `{
 "details": {"family": "qwen3", "parameter_size": "8.2B", "quantization_level": "Q4_K_M"},
 "model_info": {"general.architecture": "qwen3", "general.parameter_count": 8190735360, "qwen3.context_length": 40960},
 "capabilities": ["completion", "tools", "thinking"]
}`

func TestShowReadsParametersContextAndAbilities(t *testing.T) {
	var sent map[string]any
	srv := showServer(t, http.StatusOK, qwen3Show, &sent)
	info, err := Show(context.Background(), srv.Client(), srv.URL, "qwen3:8b")
	if err != nil {
		t.Fatal(err)
	}
	if sent["model"] != "qwen3:8b" {
		t.Errorf("request body = %v, want the model name under \"model\"", sent)
	}
	if info.ParameterCount != 8190735360 || info.ParameterSize != "8.2B" || info.Family != "qwen3" || info.ContextLength != 40960 {
		t.Errorf("info = %+v", info)
	}
	if !slices.Equal(info.Capabilities, []string{"tool-calling", "reasoning"}) || info.Embedding {
		t.Errorf("abilities = %+v", info)
	}
}

func TestShowAbilitiesCases(t *testing.T) {
	for _, tc := range []struct {
		name          string
		caps          string
		wantAbilities []string
		embedding     bool
	}{
		{"vision model", `["completion","vision"]`, []string{"vision"}, false},
		{"embedding only", `["embedding"]`, nil, true},
		{"embedding and completion", `["embedding","completion"]`, nil, false},
		{"older Ollama lists none", `null`, nil, false},
		{"unknown strings are ignored", `["completion","insert","audio"]`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := showServer(t, http.StatusOK, `{"details":{},"model_info":{},"capabilities":`+tc.caps+`}`, nil)
			info, err := Show(context.Background(), srv.Client(), srv.URL, "m")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(info.Capabilities, tc.wantAbilities) || info.Embedding != tc.embedding {
				t.Errorf("info = %+v", info)
			}
		})
	}
}

func TestShowWithoutArchitectureHasNoContext(t *testing.T) {
	srv := showServer(t, http.StatusOK, `{"model_info":{"qwen3.context_length":4096}}`, nil)
	info, err := Show(context.Background(), srv.Client(), srv.URL, "m")
	if err != nil || info.ContextLength != 0 {
		t.Errorf("info = %+v err = %v, want no context without general.architecture", info, err)
	}
}

func TestShowErrors(t *testing.T) {
	srv := showServer(t, http.StatusNotFound, `{"error":"model not found"}`, nil)
	if _, err := Show(context.Background(), srv.Client(), srv.URL, "m"); err == nil {
		t.Error("a 404 must be an error")
	}
	bad := showServer(t, http.StatusOK, `<html>`, nil)
	if _, err := Show(context.Background(), bad.Client(), bad.URL, "m"); err == nil {
		t.Error("a body that is not JSON must be an error")
	}
	if _, err := Show(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "m"); err == nil {
		t.Error("an unreachable server must be an error")
	}
}

func TestInt64OfRejectsNonNumbers(t *testing.T) {
	for _, v := range []any{nil, "12", -3.0, true} {
		if got := int64Of(v); got != 0 {
			t.Errorf("int64Of(%v) = %d, want 0", v, got)
		}
	}
	if got := int64Of(41.0); got != 41 {
		t.Errorf("int64Of(41) = %d", got)
	}
}
