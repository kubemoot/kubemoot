package ai.kubemoot.agent.tracing;

import io.opentelemetry.api.trace.Span;
import io.opentelemetry.context.Context;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

class DiscussionSpansTest {

    @Test
    void aDiscussionHasOnePhaseSpanPerPhaseAndEndsWithItsOutcome() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.opened("t1", "ops", "request");
            spans.phase("t1", "advisory", "thread_start");
            spans.phase("t1", "evaluating", "advisory");
            spans.closed("t1", "answered");

            var root = trace.span("discussion");
            assertEquals("answered", root.getAttributes().get(DiscussionSpans.OUTCOME));
            assertEquals("t1", root.getAttributes().get(DiscussionSpans.THREAD));
            assertEquals("ops", root.getAttributes().get(DiscussionSpans.CREW));
            for (String phase : List.of("phase advisory", "phase evaluating")) {
                var data = trace.span(phase);
                assertEquals(root.getSpanId(), data.getParentSpanId());
                assertEquals(root.getTraceId(), data.getTraceId());
            }
            assertEquals("advisory", trace.span("phase advisory").getAttributes().get(DiscussionSpans.PHASE));
            assertEquals("advisory", trace.span("phase evaluating").getAttributes().get(DiscussionSpans.REASON));
            assertEquals(0, spans.openCount());
        }
    }

    @Test
    void theDiscussionContinuesTheQuestionsTrace() {
        try (var trace = new TraceFixture()) {
            var question = AgentTrace.startTurn(Context.root(), "t1", "q", "gw");
            var spans = new DiscussionSpans();
            try (var ignored = question.makeCurrent()) {
                spans.opened("t1", "ops", "external");
            }
            spans.closed("t1", "answered");
            question.end();
            assertEquals(question.getSpanContext().getTraceId(), trace.span("discussion").getTraceId());
            assertEquals(question.getSpanContext().getSpanId(), trace.span("discussion").getParentSpanId());
        }
    }

    @Test
    void whoWasConvenedIsRecordedOnTheDiscussion() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.opened("t1", "ops", "request");
            spans.convened("t1", List.of("k8s-agent", "helm-agent"));
            spans.closed("t1", "answered");
            var root = trace.span("discussion");
            assertEquals(List.of("helm-agent", "k8s-agent"), root.getAttributes().get(DiscussionSpans.CONVENED));
            assertEquals(2L, root.getAttributes().get(DiscussionSpans.CONVENED_COUNT));
            assertEquals("convened", root.getEvents().get(0).getName());
        }
    }

    @Test
    void whatIsDoneInAThreadHangsUnderItsCurrentPhase() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.opened("t1", "ops", "request");
            spans.phase("t1", "evaluating", "advisory");
            try (var ignored = spans.inThread("t1")) {
                AgentTrace.tracer().spanBuilder("publish advisory_ready").startSpan().end();
            }
            spans.closed("t1", "answered");
            assertEquals(trace.span("phase evaluating").getSpanId(), trace.span("publish advisory_ready").getParentSpanId());
        }
    }

    @Test
    void anUnknownOrClosedThreadIsIgnored() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.phase("nope", "evaluating", "x");
            spans.convened("nope", List.of("a"));
            spans.closed("nope", "x");
            try (var ignored = spans.inThread("nope")) {
                assertFalse(Span.current().getSpanContext().isValid());
            }
            try (var ignored = spans.inThread(null)) {
                assertFalse(Span.current().getSpanContext().isValid());
            }
            assertTrue(trace.spans().isEmpty());
        }
    }

    @Test
    void withNoSdkNothingBreaks() {
        AgentTrace.reset();
        var spans = new DiscussionSpans();
        spans.opened("t1", null, null);
        spans.phase("t1", "advisory", null);
        spans.closed("t1", null);
    }

    @Test
    void aPhaseEnteredAfterTheCloseIsEndedAtOnce() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.opened("t1", "ops", "request");
            spans.closed("t1", "lost");
            spans.phase("t1", "evaluating", "late");
            assertEquals("lost", trace.span("discussion").getAttributes().get(DiscussionSpans.OUTCOME));
            assertTrue(trace.spans().stream().noneMatch(s -> s.getName().equals("phase evaluating")));
        }
    }

    @Test
    void aThreadWithNoIdOpensNothing() {
        try (var trace = new TraceFixture()) {
            var spans = new DiscussionSpans();
            spans.opened(null, "ops", "request");
            spans.opened(" ", "ops", "request");
            assertEquals(0, spans.openCount());
            assertTrue(trace.spans().isEmpty());
        }
    }
}
