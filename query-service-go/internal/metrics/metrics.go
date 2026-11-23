// Package metrics holds the Prometheus counters the query service exports at /metrics.
// The names and the ragsource label are the ones the one-service-per-source Python
// version exported, so dashboards and alerts carry over.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const label = "ragsource"

// Metrics is the service's counters and the registry that serves them.
type Metrics struct {
	reg       *prometheus.Registry
	requests  *prometheus.CounterVec
	successes *prometheus.CounterVec
	errors    *prometheus.CounterVec
	embedMS   *prometheus.CounterVec
	searchMS  *prometheus.CounterVec
}

// New registers the counters, with the Go runtime and process collectors.
func New() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.requests = m.counter("kubemoot_query_requests_total", "Query requests this RAG query service received")
	m.successes = m.counter("kubemoot_query_success_total", "Query requests answered")
	m.errors = m.counter("kubemoot_query_errors_total", "Query requests that failed")
	m.embedMS = m.counter("kubemoot_embedding_time_ms_total", "Time spent embedding queries, in milliseconds")
	m.searchMS = m.counter("kubemoot_search_time_ms_total", "Time spent searching the vector store, in milliseconds")
	m.reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) counter(name, help string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, []string{label})
	m.reg.MustRegister(c)
	return c
}

// Source is the counters bound to one RAGSource.
type Source struct {
	Requests, Successes, Errors, EmbedMS, SearchMS prometheus.Counter
}

// For returns the counters of a source, creating its series at zero.
func (m *Metrics) For(name string) Source {
	return Source{
		Requests:  m.requests.WithLabelValues(name),
		Successes: m.successes.WithLabelValues(name),
		Errors:    m.errors.WithLabelValues(name),
		EmbedMS:   m.embedMS.WithLabelValues(name),
		SearchMS:  m.searchMS.WithLabelValues(name),
	}
}

// Forget drops a removed source's series so a renamed source leaves nothing behind.
func (m *Metrics) Forget(name string) {
	for _, c := range []*prometheus.CounterVec{m.requests, m.successes, m.errors, m.embedMS, m.searchMS} {
		c.DeleteLabelValues(name)
	}
}

// Handler serves the Prometheus text exposition.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
