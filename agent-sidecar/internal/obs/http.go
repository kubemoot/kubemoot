package obs

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Transport returns a RoundTripper that starts a client span per request and sends the trace
// context to peer (the placement service, the gateway, the model server, the harness).
func Transport(base http.RoundTripper, peer string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base, peer: peer}
}

type transport struct {
	base http.RoundTripper
	peer string
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, span := Tracer().Start(req.Context(), t.peer+" "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("peer.service", t.peer), attribute.String("url.path", req.URL.Path)))
	defer span.End()
	req = req.Clone(ctx)
	InjectHeader(ctx, req.Header)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		Fail(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, resp.Status)
	}
	return resp, nil
}

// Handler wraps h so each request is a server span, continuing the trace context the caller sent.
// The wrapped writer keeps Flush and Unwrap, so a streamed reply is not buffered.
func Handler(h http.Handler, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := Tracer().Start(ExtractHeader(r.Context(), r.Header), name+" "+r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(sw, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", sw.status))
		if sw.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the wrapped writer.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the wrapped writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
