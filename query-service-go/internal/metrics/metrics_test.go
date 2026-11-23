package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestSeriesExistAtZeroWithHelpAndTheRagsourceLabel(t *testing.T) {
	m := New()
	m.For("docs")
	out := scrape(t, m)
	for _, want := range []string{
		"# HELP kubemoot_query_requests_total Query requests this RAG query service received",
		`kubemoot_query_requests_total{ragsource="docs"} 0`,
		`kubemoot_query_success_total{ragsource="docs"} 0`,
		`kubemoot_query_errors_total{ragsource="docs"} 0`,
		`kubemoot_embedding_time_ms_total{ragsource="docs"} 0`,
		`kubemoot_search_time_ms_total{ragsource="docs"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestCountersAreKeptPerSourceAndForgotten(t *testing.T) {
	m := New()
	m.For("a").Requests.Add(2)
	m.For("b").Requests.Inc()
	m.For("a").SearchMS.Add(40)
	out := scrape(t, m)
	if !strings.Contains(out, `kubemoot_query_requests_total{ragsource="a"} 2`) ||
		!strings.Contains(out, `kubemoot_query_requests_total{ragsource="b"} 1`) ||
		!strings.Contains(out, `kubemoot_search_time_ms_total{ragsource="a"} 40`) {
		t.Fatalf("counts wrong:\n%s", out)
	}
	m.Forget("a")
	if out = scrape(t, m); strings.Contains(out, `ragsource="a"`) || !strings.Contains(out, `ragsource="b"`) {
		t.Fatalf("forget removed the wrong series:\n%s", out)
	}
}
