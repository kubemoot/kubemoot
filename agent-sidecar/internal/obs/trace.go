// Package obs is the sidecar's observability: OpenTelemetry traces with W3C trace context through
// every hop (harness, sidecar, placement service, gateway, model server, bus), and Prometheus
// metrics under the names agent-runtime uses.
package obs

import (
	"context"
	"net/http"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/kubemoot/kubemoot/agent-sidecar"

// TraceparentHeader is the W3C header, also the NATS header and the name of the message field.
const TraceparentHeader = "traceparent"

var propagator = propagation.TraceContext{}

// Tracer is the sidecar's tracer, from the global provider.
func Tracer() trace.Tracer { return otel.Tracer(tracerName) }

// Config says how traces are produced.
type Config struct {
	// Endpoint is an OTLP/HTTP collector URL; empty keeps spans in the process (context still flows).
	Endpoint string
	// Attributes identify the pod on every span (namespace, crew, agent, harness).
	Attributes map[string]string
}

// Setup installs the global tracer provider and the W3C propagator and returns the shutdown.
func Setup(ctx context.Context, c Config) (func(context.Context) error, error) {
	attrs := make([]attribute.KeyValue, 0, len(c.Attributes)+1)
	attrs = append(attrs, attribute.String("service.name", "kubemoot-agent-sidecar"))
	for k, v := range c.Attributes {
		attrs = append(attrs, attribute.String("kubemoot."+k, v))
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewSchemaless(attrs...))}
	if c.Endpoint != "" {
		exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(c.Endpoint))
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagator)
	return tp.Shutdown, nil
}

// InjectHeader writes the context's trace context into h.
func InjectHeader(ctx context.Context, h http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(h))
}

// ExtractHeader returns ctx carrying the trace context found in h.
func ExtractHeader(ctx context.Context, h http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(h))
}

// natsCarrier adapts NATS message headers to a propagation carrier.
type natsCarrier struct{ h nats.Header }

func (c natsCarrier) Get(key string) string { return c.h.Get(key) }
func (c natsCarrier) Set(key, v string)     { c.h.Set(key, v) }
func (c natsCarrier) Keys() []string {
	keys := make([]string, 0, len(c.h))
	for k := range c.h {
		keys = append(keys, k)
	}
	return keys
}

// InjectNATS writes the context's trace context into the message's headers.
func InjectNATS(ctx context.Context, m *nats.Msg) {
	if m.Header == nil {
		m.Header = nats.Header{}
	}
	propagator.Inject(ctx, natsCarrier{m.Header})
}

// ExtractNATS returns ctx carrying the trace context in the message's headers, if any.
func ExtractNATS(ctx context.Context, m *nats.Msg) context.Context {
	if m.Header == nil {
		return ctx
	}
	return propagator.Extract(ctx, natsCarrier{m.Header})
}

// Fields returns the traceparent and tracestate of ctx for the message body fields.
func Fields(ctx context.Context) (traceparent, tracestate string) {
	h := http.Header{}
	InjectHeader(ctx, h)
	return h.Get(TraceparentHeader), h.Get("tracestate")
}

// FromFields returns ctx carrying the trace context of the body fields; empty fields change nothing.
func FromFields(ctx context.Context, traceparent, tracestate string) context.Context {
	if traceparent == "" {
		return ctx
	}
	h := http.Header{}
	h.Set(TraceparentHeader, traceparent)
	if tracestate != "" {
		h.Set("tracestate", tracestate)
	}
	return ExtractHeader(ctx, h)
}

// Fail marks the span failed with err.
func Fail(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
