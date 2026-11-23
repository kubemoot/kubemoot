package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/query-service-go/internal/embed"
	"github.com/kubemoot/kubemoot/query-service-go/internal/metrics"
	"github.com/kubemoot/kubemoot/query-service-go/internal/pgstore"
	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

// queryTimeout is a safety net for a query that never settles; callers give up sooner.
const queryTimeout = 30 * time.Second

// Result is one hit in a query answer.
type Result struct {
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata"`
	Score    float64        `json:"score"`
}

// QueryResponse is a query answer.
type QueryResponse struct {
	Query           string   `json:"query"`
	Results         []Result `json:"results"`
	Collection      string   `json:"collection"`
	EmbeddingTimeMS int64    `json:"embedding_time_ms"`
	SearchTimeMS    int64    `json:"search_time_ms"`
}

// failure is a failed search with the HTTP status /query answers it with.
type failure struct {
	status int
	detail string
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request, e *registry.Entry) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		writeBodyError(w, err)
		return
	}
	req, problems := ParseQuery(body)
	if len(problems) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": problems})
		return
	}
	resp, fail := h.runSearch(r.Context(), e, req)
	if fail != nil {
		writeDetail(w, fail.status, fail.detail)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// runSearch is the one search the /query route and the MCP search tool both answer
// through: it counts the request, embeds the question, searches, and counts the outcome.
func (h *Handler) runSearch(ctx context.Context, e *registry.Entry, req QueryRequest) (*QueryResponse, *failure) {
	counters := h.Metrics.For(e.Name)
	counters.Requests.Inc()
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	resp, err := h.search(ctx, e, req, counters)
	if err != nil {
		counters.Errors.Inc()
		return nil, h.describe(e, err)
	}
	counters.Successes.Inc()
	h.Log.Info("query answered", "source", e.Name, "queryChars", len(req.Query), "results", len(resp.Results),
		"embedMs", resp.EmbeddingTimeMS, "searchMs", resp.SearchTimeMS)
	return resp, nil
}

// search embeds the question and runs the similarity query, timing each step.
func (h *Handler) search(ctx context.Context, e *registry.Entry, req QueryRequest,
	counters metrics.Source) (*QueryResponse, error) {
	topK := e.TopK(h.DefaultTopK)
	if req.TopK != nil {
		topK = *req.TopK
	}
	start := time.Now()
	vec, err := h.Embedder.Embed(ctx, e.Embedding.Endpoint, e.Embedding.Model, req.Query)
	if err != nil {
		return nil, err
	}
	embedMS := time.Since(start).Milliseconds()
	counters.EmbedMS.Add(float64(embedMS))

	start = time.Now()
	rows, err := h.Store.Search(ctx, e, vec, topK, req.Threshold)
	if err != nil {
		return nil, err
	}
	searchMS := time.Since(start).Milliseconds()
	counters.SearchMS.Add(float64(searchMS))

	return &QueryResponse{
		Query: req.Query, Results: toResults(rows), Collection: e.VectorStore.Collection,
		EmbeddingTimeMS: embedMS, SearchTimeMS: searchMS,
	}, nil
}

func toResults(rows []pgstore.Row) []Result {
	out := make([]Result, len(rows))
	for i, row := range rows {
		out[i] = Result{Content: row.Content, Metadata: row.Metadata, Score: row.Score}
	}
	return out
}

// describe turns a failed search into its status and detail: 404 when the table is not
// there yet, 502 when the embedding server failed, 500 for anything else. Failures
// other than a missing table are logged here, with their detail.
func (h *Handler) describe(e *registry.Entry, err error) *failure {
	var unavailable *embed.UnavailableError
	switch {
	case errors.Is(err, pgstore.ErrNoTable):
		return &failure{http.StatusNotFound, notFoundDetail(e.VectorStore.Collection)}
	case errors.Is(err, context.DeadlineExceeded):
		h.Log.Error("query timed out", "source", e.Name)
		return &failure{http.StatusGatewayTimeout, "Query timed out"}
	case errors.As(err, &unavailable):
		h.Log.Error("embedding service error", "source", e.Name, "error", err)
		return &failure{http.StatusBadGateway, err.Error()}
	}
	h.Log.Error("query failed", "source", e.Name, "error", err)
	return &failure{http.StatusInternalServerError, err.Error()}
}

func notFoundDetail(collection string) string {
	return fmt.Sprintf("Collection '%s' not found. Has indexing completed?", collection)
}

// writeBodyError answers a request body that could not be read: 413 when it is over the
// limit, 400 for any other read failure (a client that went away, a broken stream).
func writeBodyError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeDetail(w, http.StatusRequestEntityTooLarge, "Request body too large")
		return
	}
	writeDetail(w, http.StatusBadRequest, "Could not read the request body")
}
