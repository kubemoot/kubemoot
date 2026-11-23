// The Kubemoot RAG query service: one process per crew serving every RAGSource of the
// crew from a registry file the operator writes. See README.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kubemoot/kubemoot/query-service-go/internal/api"
	"github.com/kubemoot/kubemoot/query-service-go/internal/embed"
	"github.com/kubemoot/kubemoot/query-service-go/internal/metrics"
	"github.com/kubemoot/kubemoot/query-service-go/internal/pgstore"
	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
	"github.com/kubemoot/kubemoot/query-service-go/internal/version"
)

const (
	defaultRegistry = "/etc/kubemoot/query/registry.json"
	defaultPort     = "8000"
	shutdownWait    = 10 * time.Second
)

func main() {
	log := newLogger(os.Getenv("LOG_LEVEL"))
	if err := run(log); err != nil {
		log.Error("query service stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := pgstore.New()
	defer store.Close()
	m := metrics.New()
	holder := &registry.Holder{}
	loader := newLoader(holder, store, m, log)

	// The service starts without a registry (it reports not ready) and picks it up when
	// it appears; an invalid one is logged and the previous one stays.
	if err := loader.Reload(); err != nil {
		log.Warn("registry not loaded yet", "path", loader.Path, "error", err)
	}
	go func() {
		if err := loader.Watch(ctx); err != nil {
			log.Error("registry watch stopped", "error", err)
		}
	}()

	h := &api.Handler{
		Registry: holder, Embedder: embed.NewClient(os.Getenv("KUBEMOOT_EMBEDDING_API_KEY")),
		Store: store, Metrics: m, DefaultTopK: defaultTopK(), Log: log,
	}
	return serve(ctx, h, log)
}

func newLoader(holder *registry.Holder, store *pgstore.Store, m *metrics.Metrics, log *slog.Logger) *registry.Loader {
	return &registry.Loader{
		Path:   envOr("KUBEMOOT_REGISTRY_FILE", defaultRegistry),
		Env:    registry.EnvFromProcess(),
		Holder: holder,
		Log:    log,
		// Pools open before the snapshot is published; if they cannot, the registry stays.
		OnSwap: func(prev, next *registry.Snapshot) error {
			if err := store.Sync(next); err != nil {
				return err
			}
			for _, name := range next.Names() {
				m.For(name)
			}
			forgetRemoved(m, prev, next)
			return nil
		},
	}
}

func forgetRemoved(m *metrics.Metrics, prev, next *registry.Snapshot) {
	if prev == nil {
		return
	}
	for _, name := range prev.Names() {
		if _, kept := next.Lookup(name); !kept {
			m.Forget(name)
		}
	}
}

func serve(ctx context.Context, h *api.Handler, log *slog.Logger) error {
	addr := envOr("KUBEMOOT_QUERY_HOST", "0.0.0.0") + ":" + envOr("KUBEMOOT_QUERY_PORT", envOr("PORT", defaultPort))
	srv := &http.Server{
		Addr: addr, Handler: h.Routes(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("query service listening", "addr", addr, "version", version.Version)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultTopK is the result count for a query that names none and whose source sets none,
// kept inside what a query accepts.
func defaultTopK() int {
	n, err := strconv.Atoi(os.Getenv("KUBEMOOT_DEFAULT_TOP_K"))
	if err != nil {
		return registry.DefaultTopK
	}
	return registry.ClampTopK(n)
}
