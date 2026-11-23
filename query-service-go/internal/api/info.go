package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// probeTimeout limits a document count or a readiness check, so a hung database cannot
// hold the operator's poll or a readiness probe.
const probeTimeout = 5 * time.Second

// info answers GET /sources/<name>/info. document_count is null when the table cannot
// be counted (missing table, database down); the operator reads null as "unknown".
func (h *Handler) info(w http.ResponseWriter, r *http.Request, e *registry.Entry) {
	var count any
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	if n, err := h.Store.Count(ctx, e); err == nil {
		count = n
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ragsource": map[string]any{"name": e.Name, "namespace": e.Namespace},
		"vectorstore": map[string]any{
			keyType: e.VectorStore.Type, "host": e.Conn.Host, "port": e.Conn.Port,
			"database": e.Conn.DBName, "collection": e.VectorStore.Collection,
			"dimensions": e.VectorStore.Dimensions,
		},
		keyEmbedding: map[string]any{keyType: "openai", "endpoint": e.Embedding.Endpoint, "model": e.Embedding.Model},
		"config":     map[string]any{"default_top_k": e.TopK(h.DefaultTopK)},
		"stats":      map[string]any{"document_count": count},
	})
}

func (h *Handler) sourceHealth(w http.ResponseWriter, _ *http.Request, e *registry.Entry) {
	writeJSON(w, http.StatusOK, map[string]string{
		keyStatus:     statusHealthy,
		"ragsource":   e.Name,
		"vectorstore": e.VectorStore.Type + ":" + e.VectorStore.Collection,
		keyEmbedding:  e.Embedding.Model,
	})
}

// sourceReady checks the source's database and embedding server; 503 names what is down.
func (h *Handler) sourceReady(w http.ResponseWriter, r *http.Request, e *registry.Entry) {
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	var failures []string
	if err := h.Store.Ping(ctx, e); err != nil {
		failures = append(failures, "database: "+err.Error())
	}
	if err := h.Embedder.Ping(ctx, e.Embedding.Endpoint); err != nil {
		failures = append(failures, "embedding: "+reason(err))
	}
	if len(failures) > 0 {
		writeDetail(w, http.StatusServiceUnavailable, strings.Join(failures, "; "))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		keyStatus: statusReady, "checks": map[string]string{"database": "ok", keyEmbedding: "ok"},
	})
}

// reason is the underlying cause of a wrapped error, or the error itself.
func reason(err error) string {
	if cause := errors.Unwrap(err); cause != nil {
		return cause.Error()
	}
	return err.Error()
}
