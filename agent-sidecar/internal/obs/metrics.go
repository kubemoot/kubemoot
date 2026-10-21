package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// callBuckets are the bounds agent-runtime's scheduler timers use: a second to ten minutes.
var callBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600}

// Labels are on every series: the same three for any harness.
type Labels struct{ Namespace, Crew, Agent string }

// Metrics are the sidecar's Prometheus series. The scheduler series keep agent-runtime's names
// so the existing dashboards keep working; the kubemoot_sidecar_ series are the contract metrics
// of the sidecar's own traffic.
type Metrics struct {
	Registry *prometheus.Registry

	calls        *prometheus.CounterVec
	callSeconds  *prometheus.HistogramVec
	genTokens    *prometheus.CounterVec
	waitSeconds  *prometheus.HistogramVec
	turns        *prometheus.CounterVec
	turnSeconds  prometheus.Histogram
	signals      *prometheus.CounterVec
	progressAcks prometheus.Counter
	deliveries   *prometheus.CounterVec
	doorRequests *prometheus.CounterVec
	policy       string
}

// New registers the series on a fresh registry. policy is the placement policy for the calls series.
func New(l Labels, policy string) *Metrics {
	if policy == "" {
		policy = "algorithm"
	}
	c := prometheus.Labels{"namespace": l.Namespace, "crew": l.Crew, "agent": l.Agent}
	m := &Metrics{Registry: prometheus.NewRegistry(), policy: policy}
	m.calls = counter(c, "kubemoot_scheduler_calls_total",
		"Model calls this agent made, by placement policy, server, model, call kind, start and outcome",
		"policy", "server", "model", "kind", "start", "outcome")
	m.callSeconds = histogram(c, "kubemoot_scheduler_call_seconds",
		"Duration of model calls, from sending the request to the reply or failure", "server", "model", "kind")
	m.genTokens = counter(c, "kubemoot_scheduler_generated_tokens_total",
		"Tokens the engine generated for this agent's successful calls, by server and model", "server", "model")
	m.waitSeconds = histogram(c, "kubemoot_scheduler_capacity_wait_seconds",
		"Time a call waited for an accelerator with room, by outcome", "server", "model", "outcome")
	m.turns = counter(c, "kubemoot_sidecar_turns_total", "Turns handed to the harness, by signal sent", "signal")
	m.turnSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "kubemoot_sidecar_turn_seconds",
		Help: "Time from handing a turn to the harness to its reply", Buckets: callBuckets, ConstLabels: c})
	m.signals = counter(c, "kubemoot_sidecar_signals_total", "Signals the sidecar published, by type", "type")
	m.progressAcks = prometheus.NewCounter(prometheus.CounterOpts{Name: "kubemoot_sidecar_progress_acks_total",
		Help: "Messages reported as still in progress to JetStream", ConstLabels: c})
	m.deliveries = counter(c, "kubemoot_sidecar_deliveries_total",
		"Bus messages received, by what became of them: turn, duplicate, not_selected, ignored", "result")
	m.doorRequests = counter(c, "kubemoot_sidecar_door_requests_total",
		"Requests to the model and tools doors, by door and result", "door", "result")
	m.Registry.MustRegister(m.calls, m.callSeconds, m.genTokens, m.waitSeconds, m.turns, m.turnSeconds,
		m.signals, m.progressAcks, m.deliveries, m.doorRequests)
	return m
}

func counter(c prometheus.Labels, name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help, ConstLabels: c}, labels)
}

func histogram(c prometheus.Labels, name, help string, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: callBuckets,
		ConstLabels: c}, labels)
}

// Result names an outcome for the door counters.
func Result(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

// Handler serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// ModelCall is one finished model call.
type ModelCall struct {
	Server, Model, Kind string
	Cold, OK            bool
	Duration            time.Duration
	OutputTokens        int64
}

// Call records a model call.
func (m *Metrics) Call(c ModelCall) {
	start, outcome := "warm", "ok"
	if c.Cold {
		start = "cold"
	}
	if !c.OK {
		outcome = "failed"
	}
	m.calls.WithLabelValues(m.policy, c.Server, c.Model, c.Kind, start, outcome).Inc()
	m.callSeconds.WithLabelValues(c.Server, c.Model, c.Kind).Observe(c.Duration.Seconds())
	if c.OK && c.OutputTokens > 0 {
		m.genTokens.WithLabelValues(c.Server, c.Model).Add(float64(c.OutputTokens))
	}
}

// Waited records a call that waited for capacity; server is empty when it was not placed.
func (m *Metrics) Waited(server, model string, d time.Duration) {
	outcome := "placed"
	if server == "" {
		outcome = "not_placed"
	}
	m.waitSeconds.WithLabelValues(server, model, outcome).Observe(d.Seconds())
}

// Turn records one finished turn and the signal it ended in.
func (m *Metrics) Turn(signal string, d time.Duration) {
	m.turns.WithLabelValues(signal).Inc()
	m.turnSeconds.Observe(d.Seconds())
}

// Signal counts a published signal.
func (m *Metrics) Signal(t string) { m.signals.WithLabelValues(t).Inc() }

// ProgressAck counts one in-progress acknowledgement.
func (m *Metrics) ProgressAck() { m.progressAcks.Inc() }

// Delivery counts a received bus message by result.
func (m *Metrics) Delivery(result string) { m.deliveries.WithLabelValues(result).Inc() }

// Door counts a request to a door by result.
func (m *Metrics) Door(door, result string) { m.doorRequests.WithLabelValues(door, result).Inc() }
