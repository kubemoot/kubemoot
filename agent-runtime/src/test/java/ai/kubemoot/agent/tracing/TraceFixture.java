package ai.kubemoot.agent.tracing;

import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;

import java.util.List;

/** An SDK tracer that keeps finished spans in memory, installed as the runtime's tracer. */
public final class TraceFixture implements AutoCloseable {

    private final InMemorySpanExporter exporter = InMemorySpanExporter.create();
    private final SdkTracerProvider provider = SdkTracerProvider.builder()
            .addSpanProcessor(SimpleSpanProcessor.create(exporter)).build();

    public TraceFixture() {
        AgentTrace.install(provider.get("test"));
    }

    public List<SpanData> spans() {
        return exporter.getFinishedSpanItems();
    }

    public SpanData span(String name) {
        return spans().stream().filter(s -> s.getName().equals(name)).findFirst()
                .orElseThrow(() -> new AssertionError("no span " + name + " in " + spans().stream()
                        .map(SpanData::getName).toList()));
    }

    @Override
    public void close() {
        AgentTrace.reset();
        System.clearProperty(AgentTrace.CONTENT_PROPERTY);
        provider.close();
    }
}
