package vllm

import (
	"context"
	"net/http"
	"testing"
)

func TestServedModelsReadsTheContextWindow(t *testing.T) {
	srv := serveModelsPage(t, http.StatusOK, `{"data":[{"id":"qwen3:14b","max_model_len":16384},{"id":"b"}]}`)
	got, err := New(srv.Client()).ServedModels(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].MaxModelLen != 16384 || got[1].MaxModelLen != 0 {
		t.Errorf("served models = %+v", got)
	}
}
