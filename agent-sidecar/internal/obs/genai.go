package obs

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// GenAI attribute names, from the OpenTelemetry GenAI semantic conventions. The native agent
// writes the same names, so a model call looks alike whichever harness made it.
const (
	attrOperation    = "gen_ai.operation.name"
	attrProvider     = "gen_ai.provider.name"
	attrRequestModel = "gen_ai.request.model"
	attrInputTokens  = "gen_ai.usage.input_tokens"
	attrOutputTokens = "gen_ai.usage.output_tokens"
	attrServer       = "kubemoot.server"
	providerOpenAI   = "openai"
)

// ChatResult is what one model call did, for its span.
type ChatResult struct {
	// Model is the model the call ran on; Server the model server that placed it.
	Model, Server string
	// InputTokens and OutputTokens are the usage the reply reported; zero when it reported none.
	InputTokens, OutputTokens int64
	// Err is the transport error, nil when the server answered.
	Err error
}

// StartChat starts the span of one model call, named "chat <model>" as the native agent's is. The
// call's prompt and reply are never put in it: the sidecar carries no content.
func StartChat(ctx context.Context, model string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "chat "+model, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String(attrOperation, "chat"), attribute.String(attrProvider, providerOpenAI),
			attribute.String(attrRequestModel, model)))
}

// EndChat adds what the call did to its span and ends it.
func EndChat(span trace.Span, r ChatResult) {
	if r.Server != "" {
		span.SetAttributes(attribute.String(attrServer, r.Server))
	}
	if r.InputTokens > 0 || r.OutputTokens > 0 {
		span.SetAttributes(attribute.Int64(attrInputTokens, r.InputTokens), attribute.Int64(attrOutputTokens, r.OutputTokens))
	}
	if r.Err != nil {
		Fail(span, r.Err)
	}
	span.End()
}
