package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/kubemoot/kubemoot/query-service-go/internal/embed"
	"github.com/kubemoot/kubemoot/query-service-go/internal/metrics"
	"github.com/kubemoot/kubemoot/query-service-go/internal/pgstore"
	"github.com/kubemoot/kubemoot/query-service-go/internal/registry"
)

type fakeEmbedder struct {
	vec      []float64
	err      error
	pingErr  error
	endpoint string
	model    string
	text     string
}

func (f *fakeEmbedder) Embed(_ context.Context, endpoint, model, text string) ([]float64, error) {
	f.endpoint, f.model, f.text = endpoint, model, text
	return f.vec, f.err
}

func (f *fakeEmbedder) Ping(context.Context, string) error { return f.pingErr }

type fakeBackend struct {
	rows       []pgstore.Row
	err        error
	count      int64
	countErr   error
	pingErr    error
	gotTopK    int
	gotThresh  *float64
	gotVec     []float64
	gotCollect string
}

func (f *fakeBackend) Search(_ context.Context, e *registry.Entry, vec []float64, topK int, th *float64) ([]pgstore.Row, error) {
	f.gotVec, f.gotTopK, f.gotThresh, f.gotCollect = vec, topK, th, e.VectorStore.Collection
	return f.rows, f.err
}

func (f *fakeBackend) Count(context.Context, *registry.Entry) (int64, error) {
	return f.count, f.countErr
}
func (f *fakeBackend) Ping(context.Context, *registry.Entry) error { return f.pingErr }

const testRegistry = `{"sources":[
 {"name":"docs","namespace":"pilot","vectorStore":{"endpoint":"pg:5432/vectors","collection":"docs-v2","dimensions":768},
  "embedding":{"endpoint":"http://embed:8000","model":"nomic"},"defaultTopK":7},
 {"name":"resumes","namespace":"pilot","vectorStore":{"endpoint":"pg:5432/vectors","collection":"resumes","dimensions":768},
  "embedding":{"endpoint":"http://embed:8000","model":"nomic"}}]}`

type rig struct {
	h   *Handler
	emb *fakeEmbedder
	be  *fakeBackend
}

func newRig(t *testing.T) *rig {
	t.Helper()
	holder := loadRegistry(t)
	snap := holder.Load()
	r := &rig{emb: &fakeEmbedder{vec: []float64{1, 0}}, be: &fakeBackend{}}
	m := metrics.New()
	for _, n := range snap.Names() {
		m.For(n)
	}
	r.h = &Handler{Registry: holder, Embedder: r.emb, Store: r.be, Metrics: m, DefaultTopK: 5,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return r
}

// loadRegistry loads testRegistry into a holder through the Loader, the only way in.
func loadRegistry(t *testing.T) *registry.Holder {
	t.Helper()
	path := t.TempDir() + "/registry.json"
	if err := writeFile(path, testRegistry); err != nil {
		t.Fatal(err)
	}
	h := &registry.Holder{}
	l := &registry.Loader{Path: path, Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	return h
}

func (r *rig) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.h.Routes().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return out
}

// queryOnce sends one query with every field set and returns the rig and the answer.
func queryOnce(t *testing.T) (*rig, *httptest.ResponseRecorder) {
	t.Helper()
	r := newRig(t)
	r.be.rows = []pgstore.Row{{Content: "c1", Metadata: map[string]any{"k": "v"}, Score: 0.9}, {Content: "c2", Metadata: map[string]any{}, Score: 0.5}}
	rec := r.do(http.MethodPost, "/sources/docs/query", `{"query":"what runs <here>","top_k":2,"threshold":0.4,"filter":{"a":1}}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return r, rec
}

func TestQueryAnswersWithThePythonShape(t *testing.T) {
	_, rec := queryOnce(t)
	body := decodeBody(t, rec)
	for _, k := range []string{"query", "results", "collection", "embedding_time_ms", "search_time_ms"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing %s in %v", k, body)
		}
	}
	if body[fieldQuery] != "what runs <here>" || body["collection"] != collDocs {
		t.Errorf("body %v", body)
	}
	first := body["results"].([]any)[0].(map[string]any)
	if first["content"] != "c1" || first["score"] != 0.9 || first["metadata"].(map[string]any)["k"] != "v" {
		t.Errorf("first %v", first)
	}
	if strings.Contains(rec.Body.String(), "u003") {
		t.Errorf("HTML characters must not be escaped: %s", rec.Body)
	}
}

func TestQueryPassesTheRequestToTheEmbedderAndTheStore(t *testing.T) {
	r, _ := queryOnce(t)
	if r.be.gotTopK != 2 || r.be.gotThresh == nil || *r.be.gotThresh != 0.4 || r.be.gotCollect != collDocs {
		t.Errorf("backend got topK=%d threshold=%v collection=%s", r.be.gotTopK, r.be.gotThresh, r.be.gotCollect)
	}
	if r.emb.endpoint != "http://embed:8000" || r.emb.model != "nomic" || r.emb.text != "what runs <here>" {
		t.Errorf("embedder got %+v", r.emb)
	}
}

func TestQueryUsesTheSourceDefaultThenTheServiceDefaultForTopK(t *testing.T) {
	r := newRig(t)
	r.do(http.MethodPost, "/sources/docs/query", `{"query":"q"}`)
	if r.be.gotTopK != 7 || r.be.gotThresh != nil {
		t.Errorf("source default: %d %v", r.be.gotTopK, r.be.gotThresh)
	}
	r.do(http.MethodPost, "/sources/resumes/query", `{"query":"q","top_k":null,"threshold":null}`)
	if r.be.gotTopK != 5 || r.be.gotThresh != nil {
		t.Errorf("service default: %d %v", r.be.gotTopK, r.be.gotThresh)
	}
}

func TestQueryWithNoHitsReturnsAnEmptyList(t *testing.T) {
	r := newRig(t)
	r.be.rows = nil
	rec := r.do(http.MethodPost, "/sources/docs/query", `{"query":"q"}`)
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("%s", rec.Body)
	}
}

func TestQueryErrorsMapToTheStatusCodesAndCount(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*rig)
		status int
		detail string
	}{
		{"missing table", func(r *rig) { r.be.err = fmt.Errorf("x: %w", pgstore.ErrNoTable) }, 404,
			"Collection 'docs-v2' not found. Has indexing completed?"},
		{"embedding down", func(r *rig) { r.emb.err = &embed.UnavailableError{Err: errors.New("refused")} }, 502,
			"Embedding service error: refused"},
		{"embedding garbage", func(r *rig) { r.emb.err = errors.New("embedding response has no data") }, 500,
			"embedding response has no data"},
		{"database", func(r *rig) { r.be.err = errors.New("boom") }, 500, "boom"},
	}
	for _, c := range cases {
		r := newRig(t)
		c.setup(r)
		rec := r.do(http.MethodPost, "/sources/docs/query", `{"query":"q"}`)
		if rec.Code != c.status || decodeBody(t, rec)["detail"] != c.detail {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		metricsText := r.do(http.MethodGet, "/metrics", "").Body.String()
		for _, want := range []string{
			`kubemoot_query_requests_total{ragsource="docs"} 1`,
			`kubemoot_query_errors_total{ragsource="docs"} 1`,
			`kubemoot_query_success_total{ragsource="docs"} 0`,
		} {
			if !strings.Contains(metricsText, want) {
				t.Errorf("%s: metrics lack %s", c.name, want)
			}
		}
	}
}

func TestASuccessfulQueryCountsRequestAndSuccess(t *testing.T) {
	r := newRig(t)
	r.do(http.MethodPost, "/sources/docs/query", `{"query":"q"}`)
	out := r.do(http.MethodGet, "/metrics", "").Body.String()
	for _, want := range []string{
		`kubemoot_query_requests_total{ragsource="docs"} 1`,
		`kubemoot_query_success_total{ragsource="docs"} 1`,
		`kubemoot_query_errors_total{ragsource="docs"} 0`,
		`kubemoot_query_requests_total{ragsource="resumes"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestInvalidBodiesAre422AndNotCounted(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{`{}`, `{"query":1}`, `{"query":"q","top_k":0}`, `{"query":"q","threshold":2}`, `[]`, `nope`, ``} {
		rec := r.do(http.MethodPost, "/sources/docs/query", body)
		if rec.Code != 422 {
			t.Errorf("%q: %d", body, rec.Code)
		}
		if _, ok := decodeBody(t, rec)["detail"].([]any); !ok {
			t.Errorf("%q: detail is not a list: %s", body, rec.Body)
		}
	}
	if out := r.do(http.MethodGet, "/metrics", "").Body.String(); !strings.Contains(out, `kubemoot_query_requests_total{ragsource="docs"} 0`) {
		t.Error("a validation failure must not count as a query")
	}
}

func TestRoutingErrors(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		method, path string
		status       int
		detail       string
	}{
		{http.MethodPost, "/sources/nope/query", 404, "Source 'nope' not found"},
		{http.MethodGet, "/sources/nope/info", 404, "Source 'nope' not found"},
		{http.MethodGet, "/sources/docs/query", 405, msgNotAllowed},
		{http.MethodPost, "/sources/docs/info", 405, msgNotAllowed},
		{http.MethodGet, "/sources/docs/other", 404, msgNotFound},
		{http.MethodGet, "/query", 404, msgNotFound},
		{http.MethodPost, "/health", 405, msgNotAllowed},
		{http.MethodGet, "/", 404, msgNotFound},
	}
	for _, c := range cases {
		rec := r.do(c.method, c.path, "")
		if rec.Code != c.status || decodeBody(t, rec)["detail"] != c.detail {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestNothingIsServedBeforeTheRegistryLoads(t *testing.T) {
	r := newRig(t)
	r.h.Registry = &registry.Holder{}
	for _, p := range [][2]string{{http.MethodPost, "/sources/docs/query"}, {http.MethodGet, "/sources/docs/info"}, {http.MethodGet, "/ready"}} {
		if rec := r.do(p[0], p[1], `{"query":"q"}`); rec.Code != 503 {
			t.Errorf("%v: %d", p, rec.Code)
		}
	}
	if rec := r.do(http.MethodGet, "/health", ""); rec.Code != 200 {
		t.Errorf("health: %d", rec.Code)
	}
}

func TestCrewLevelEndpoints(t *testing.T) {
	r := newRig(t)
	health := decodeBody(t, r.do(http.MethodGet, "/health", ""))
	if health["status"] != "healthy" {
		t.Errorf("health %v", health)
	}
	v := decodeBody(t, r.do(http.MethodGet, "/version", ""))
	if v["service"] != "kubemoot-query-service" || v["version"] == "" {
		t.Errorf("version %v", v)
	}
	ready := decodeBody(t, r.do(http.MethodGet, "/ready", ""))
	reg := ready["registry"].(map[string]any)
	srcs := reg["sources"].(map[string]any)
	if ready["status"] != "ready" || reg["version"] == "" || srcs["docs"] != collDocs || srcs["resumes"] != "resumes" {
		t.Errorf("ready %v", ready)
	}
	if rec := r.do(http.MethodGet, "/metrics", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "kubemoot_query_requests_total") {
		t.Errorf("metrics %d", rec.Code)
	}
}

func TestReadyStaysUpWhenASourceDatabaseIsDown(t *testing.T) {
	r := newRig(t)
	r.be.pingErr = errors.New("db down")
	if rec := r.do(http.MethodGet, "/ready", ""); rec.Code != 200 {
		t.Fatalf("crew ready: %d", rec.Code)
	}
	rec := r.do(http.MethodGet, "/sources/docs/ready", "")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "database: db down") {
		t.Fatalf("source ready: %d %s", rec.Code, rec.Body)
	}
}

func TestSourceReadyNamesEachFailure(t *testing.T) {
	r := newRig(t)
	r.be.pingErr = errors.New("db down")
	r.emb.pingErr = &embed.UnavailableError{Err: errors.New("refused")}
	rec := r.do(http.MethodGet, "/sources/docs/ready", "")
	if got := decodeBody(t, rec)["detail"]; rec.Code != 503 || got != "database: db down; embedding: refused" {
		t.Fatalf("%d %v", rec.Code, got)
	}
	r.be.pingErr, r.emb.pingErr = nil, nil
	rec = r.do(http.MethodGet, "/sources/docs/ready", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"checks":{"database":"ok","embedding":"ok"}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSourceHealthBody(t *testing.T) {
	got := decodeBody(t, newRig(t).do(http.MethodGet, "/sources/docs/health", ""))
	want := map[string]any{"status": "healthy", "ragsource": "docs", "vectorstore": "pgvector:docs-v2", "embedding": "nomic"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v want %v", k, got[k], v)
		}
	}
}

func TestInfoBody(t *testing.T) {
	r := newRig(t)
	r.be.count = 42
	got := decodeBody(t, r.do(http.MethodGet, "/sources/docs/info", ""))
	vs := got["vectorstore"].(map[string]any)
	if got["ragsource"].(map[string]any)["namespace"] != "pilot" || vs["collection"] != collDocs ||
		vs["host"] != "pg" || vs["port"] != float64(5432) || vs["database"] != "vectors" || vs["dimensions"] != float64(768) ||
		got["embedding"].(map[string]any)["type"] != "openai" || got["config"].(map[string]any)["default_top_k"] != float64(7) ||
		got["stats"].(map[string]any)["document_count"] != float64(42) {
		t.Fatalf("%v", got)
	}
}

func TestInfoReportsAnUnknownCountAsNull(t *testing.T) {
	r := newRig(t)
	r.be.countErr = pgstore.ErrNoTable
	rec := r.do(http.MethodGet, "/sources/docs/info", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"stats":{"document_count":null}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAnOversizedBodyIs413(t *testing.T) {
	r := newRig(t)
	rec := r.do(http.MethodPost, "/sources/docs/query", `{"query":"`+strings.Repeat("a", maxRequestBody)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", rec.Code)
	}
}

const (
	collDocs      = "docs-v2"
	msgNotAllowed = "Method Not Allowed"
	msgNotFound   = "Not Found"
	typeGTE       = "greater_than_equal"
	typeLTE       = "less_than_equal"
	typeDict      = "dict_type"
	typeJSON      = "json_invalid"
	typeModel     = "model_attributes_type"
)

// JSON keys the tests repeat.
const (
	fieldQuery  = "query"
	keyFilePath = "file_path"
	keySource   = "source"
	keyURL      = "url"
)

func TestAnUnreadableBodyIs400AndAnOversizedOneIs413(t *testing.T) {
	r := newRig(t)
	req := httptest.NewRequest(http.MethodPost, "/sources/docs/query", iotest.ErrReader(errors.New("connection reset")))
	rec := httptest.NewRecorder()
	r.h.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unreadable body: %d", rec.Code)
	}
}

func TestAQueryThatRunsOutOfTimeIs504(t *testing.T) {
	r := newRig(t)
	r.be.err = fmt.Errorf("search: %w", context.DeadlineExceeded)
	rec := r.do(http.MethodPost, "/sources/docs/query", `{"query":"q"}`)
	if rec.Code != http.StatusGatewayTimeout || decodeBody(t, rec)["detail"] != "Query timed out" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
