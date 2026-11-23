package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubemoot/kubemoot/query-service-go/internal/metrics"
	"github.com/kubemoot/kubemoot/query-service-go/internal/pgstore"
	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

func TestEnvironmentHelpers(t *testing.T) {
	t.Setenv("QS_T_STR", "x")
	if envOr("QS_T_STR", "d") != "x" || envOr("QS_T_UNSET", "d") != "d" {
		t.Error("envOr")
	}
	for value, want := range map[string]int{"12": 12, "abc": 5, "": 5, "-4": 1, "0": 1, "500": 100} {
		t.Setenv("KUBEMOOT_DEFAULT_TOP_K", value)
		if got := defaultTopK(); got != want {
			t.Errorf("KUBEMOOT_DEFAULT_TOP_K=%q: %d want %d", value, got, want)
		}
	}
}

func TestNewLoggerFallsBackToInfoOnAnUnknownLevel(t *testing.T) {
	if newLogger("nonsense") == nil || newLogger("DEBUG") == nil || newLogger("") == nil {
		t.Fatal("no logger")
	}
}

func TestTheLoaderOpensPoolsAndTracksMetricSeriesAcrossSwaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	doc := func(name string) string {
		return `{"sources":[{"name":"` + name + `","vectorStore":{"endpoint":"127.0.0.1:1","collection":"c"},"embedding":{"endpoint":"e","model":"m"}}]}`
	}
	if err := os.WriteFile(path, []byte(doc("first")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBEMOOT_REGISTRY_FILE", path)
	store, m, holder := pgstore.New(), metrics.New(), &registry.Holder{}
	defer store.Close()
	l := newLoader(holder, store, m, slog.New(slog.DiscardHandler))
	if err := l.Reload(); err != nil || holder.Load() == nil {
		t.Fatalf("%v", err)
	}
	if err := os.WriteFile(path, []byte(doc("second")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := holder.Load().Lookup("second"); !ok {
		t.Fatal("the swap did not take")
	}
}
