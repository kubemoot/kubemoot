package embed

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type seen struct {
	path, auth, ctype string
	body              request
}

func fakeServer(t *testing.T, status int, reply string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth, got.ctype = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestEmbedPostsOpenAIRequestAndNormalizes(t *testing.T) {
	srv, got := fakeServer(t, 200, `{"data":[{"embedding":[3.0,0.0,4.0]}]}`)
	vec, err := NewClient("").Embed(context.Background(), srv.URL, "nomic", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/embeddings" || got.body.Model != "nomic" || got.body.Input != "hello" || got.ctype != "application/json" {
		t.Fatalf("request %+v", got)
	}
	if got.auth != "" {
		t.Errorf("no key configured, got Authorization %q", got.auth)
	}
	want := []float64{0.6, 0, 0.8}
	for i := range want {
		if math.Abs(vec[i]-want[i]) > 1e-12 {
			t.Fatalf("got %v want %v", vec, want)
		}
	}
}

func TestEmbedSendsTheAPIKeyAsABearerToken(t *testing.T) {
	srv, got := fakeServer(t, 200, `{"data":[{"embedding":[1]}]}`)
	if _, err := NewClient("k3y").Embed(context.Background(), srv.URL, "m", "x"); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer k3y" {
		t.Fatalf("Authorization %q", got.auth)
	}
}

func TestEmbedErrorsAreUnavailableOnlyForTheServer(t *testing.T) {
	var unavailable *UnavailableError
	srv, _ := fakeServer(t, 400, `{"error":"no"}`)
	if _, err := NewClient("").Embed(context.Background(), srv.URL, "m", "x"); !errors.As(err, &unavailable) {
		t.Errorf("a 400 answer: %v", err)
	}
	srv.Close()
	if _, err := NewClient("").Embed(context.Background(), srv.URL, "m", "x"); !errors.As(err, &unavailable) {
		t.Errorf("a refused connection: %v", err)
	}
	for name, reply := range map[string]string{"not json": `<html>`, "no data": `{"data":[]}`} {
		bad, _ := fakeServer(t, 200, reply)
		_, err := NewClient("").Embed(context.Background(), bad.URL, "m", "x")
		if err == nil || errors.As(err, &unavailable) {
			t.Errorf("%s: want a plain error, got %v", name, err)
		}
	}
}

func TestPingUsesTheModelsRoute(t *testing.T) {
	srv, got := fakeServer(t, 200, `{"data":[]}`)
	if err := NewClient("").Ping(context.Background(), srv.URL); err != nil || got.path != "/v1/models" {
		t.Fatalf("%v, path %s", err, got.path)
	}
	down, _ := fakeServer(t, 503, ``)
	if err := NewClient("").Ping(context.Background(), down.URL); err == nil {
		t.Fatal("a 503 is not ready")
	}
}

func TestL2NormalizeMatchesTheSharedGoldenVector(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/normalization-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Input, Expected []float64 }
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	got := L2Normalize(golden.Input)
	if len(got) != len(golden.Expected) {
		t.Fatal("length differs")
	}
	for i := range got {
		if math.Abs(got[i]-golden.Expected[i]) > 1e-6 {
			t.Errorf("[%d] %f vs %f", i, got[i], golden.Expected[i])
		}
	}
}

func TestL2NormalizeLeavesZeroAndEmptyAlone(t *testing.T) {
	if got := L2Normalize([]float64{0, 0}); got[0] != 0 || got[1] != 0 {
		t.Errorf("zero vector: %v", got)
	}
	if got := L2Normalize(nil); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
}

func TestL2NormalizeDoesNotChangeItsInput(t *testing.T) {
	in := []float64{3, 4}
	_ = L2Normalize(in)
	if in[0] != 3 || in[1] != 4 {
		t.Fatal("input changed")
	}
}
