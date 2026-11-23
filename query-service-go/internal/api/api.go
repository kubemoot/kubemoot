// Package api serves the query service's HTTP routes: per-source query, info, health
// and ready under /sources/<name>/, and the crew-level health, ready, version and
// metrics. Request and response bodies and status codes are the Python service's.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/kubemoot/kubemoot/query-service-go/internal/metrics"
	"github.com/kubemoot/kubemoot/query-service-go/internal/pgstore"
	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
	"github.com/kubemoot/kubemoot/query-service-go/internal/version"
)

const maxRequestBody = 4 << 20

// Names the JSON answers and the source routes share.
const (
	opQuery       = "query"
	keyType       = "type"
	opInfo        = "info"
	opHealth      = "health"
	opReady       = "ready"
	opMCP         = "mcp"
	anyMethod     = ""
	keyStatus     = "status"
	keyEmbedding  = "embedding"
	statusReady   = "ready"
	statusHealthy = "healthy"
)

// Embedder embeds a question and checks that an embedding server is up.
type Embedder interface {
	Embed(ctx context.Context, endpoint, model, text string) ([]float64, error)
	Ping(ctx context.Context, endpoint string) error
}

// Backend searches a source's vector store.
type Backend interface {
	Search(ctx context.Context, e *registry.Entry, vec []float64, topK int, threshold *float64) ([]pgstore.Row, error)
	Count(ctx context.Context, e *registry.Entry) (int64, error)
	Ping(ctx context.Context, e *registry.Entry) error
}

// Handler is the service's HTTP surface.
type Handler struct {
	Registry    *registry.Holder
	Embedder    Embedder
	Store       Backend
	Metrics     *metrics.Metrics
	DefaultTopK int
	Log         *slog.Logger

	mcpCache mcpCache
}

// sourceOp is one route under /sources/<name>/; an empty method accepts any method.
type sourceOp struct {
	method string
	run    func(h *Handler, w http.ResponseWriter, r *http.Request, e *registry.Entry)
}

var sourceOps = map[string]sourceOp{
	opQuery:  {http.MethodPost, (*Handler).query},
	opInfo:   {http.MethodGet, (*Handler).info},
	opHealth: {http.MethodGet, (*Handler).sourceHealth},
	opReady:  {http.MethodGet, (*Handler).sourceReady},
	opMCP:    {anyMethod, (*Handler).mcp},
}

// Routes returns the service's handler.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sources/{name}/{op}", h.sourceRoute)
	mux.HandleFunc("/health", only(http.MethodGet, h.health))
	mux.HandleFunc("/ready", only(http.MethodGet, h.ready))
	mux.HandleFunc("/version", only(http.MethodGet, h.version))
	mux.Handle("/metrics", h.Metrics.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { writeDetail(w, http.StatusNotFound, "Not Found") })
	return mux
}

// only answers 405 in the service's JSON error shape for any other method.
func only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeDetail(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return
		}
		next(w, r)
	}
}

func (h *Handler) sourceRoute(w http.ResponseWriter, r *http.Request) {
	op, ok := sourceOps[r.PathValue("op")]
	if !ok {
		writeDetail(w, http.StatusNotFound, "Not Found")
		return
	}
	run := func(w http.ResponseWriter, r *http.Request) {
		e, found := h.entry(w, r.PathValue("name"))
		if found {
			op.run(h, w, r, e)
		}
	}
	if op.method == anyMethod {
		run(w, r)
		return
	}
	only(op.method, run)(w, r)
}

// entry finds the named source in the current snapshot, answering 503 before the first
// registry load and 404 for a name the registry does not list.
func (h *Handler) entry(w http.ResponseWriter, name string) (*registry.Entry, bool) {
	snap := h.Registry.Load()
	if snap == nil {
		writeDetail(w, http.StatusServiceUnavailable, "Registry not loaded")
		return nil, false
	}
	e, ok := snap.Lookup(name)
	if !ok {
		writeDetail(w, http.StatusNotFound, "Source '"+name+"' not found")
	}
	return e, ok
}

func (h *Handler) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version, "service": version.Service})
}

// health says the process is up; it does not depend on the registry or any backend.
func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{keyStatus: statusHealthy})
}

// ready reports the registry generation this pod serves and each source's collection,
// so a rollout can wait until the pod serves the collection it expects. A bad source
// database does not make the pod unready; the per-source /ready checks that.
func (h *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	snap := h.Registry.Load()
	if snap == nil {
		writeDetail(w, http.StatusServiceUnavailable, "Registry not loaded")
		return
	}
	sources := map[string]string{}
	for name, e := range snap.Entries {
		sources[name] = e.VectorStore.Collection
	}
	writeJSON(w, http.StatusOK, map[string]any{
		keyStatus:  statusReady,
		"registry": map[string]any{"version": snap.Version, "sources": sources},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"detail":"response encoding failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeDetail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
