package ai.kubemoot.agent.tracing;

import io.opentelemetry.api.common.AttributeKey;
import io.opentelemetry.api.common.Attributes;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.Scope;

import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/**
 * The spans no library knows: the discussion, the phases it passes through, and who was
 * convened. The coordinator keeps one {@code discussion} span per thread it runs, from the
 * thread opening to its close, with one child {@code phase <name>} span per phase. Every span an
 * agent makes for the thread, here or in a sidecar, hangs under it, because the coordinator's
 * messages carry the current phase's trace context.
 *
 * <p>A thread the coordinator loses to a restart leaves no closing span; the replica that takes
 * it up starts a new {@code discussion} span in the same trace.</p>
 */
public final class DiscussionSpans {

    static final AttributeKey<String> THREAD = Attrs.THREAD;
    static final AttributeKey<String> CREW = AttributeKey.stringKey("kubemoot.crew");
    static final AttributeKey<String> ORIGIN = AttributeKey.stringKey("kubemoot.origin");
    static final AttributeKey<String> PHASE = AttributeKey.stringKey("kubemoot.phase");
    static final AttributeKey<String> REASON = AttributeKey.stringKey("kubemoot.phase.reason");
    static final AttributeKey<String> OUTCOME = AttributeKey.stringKey("kubemoot.outcome");
    static final AttributeKey<List<String>> CONVENED = AttributeKey.stringArrayKey("kubemoot.convened");
    static final AttributeKey<Long> CONVENED_COUNT = AttributeKey.longKey("kubemoot.convened.count");

    private static final class Live {
        final Span root;
        Span phase;
        boolean finished;

        Live(Span root) {
            this.root = root;
        }

        /** Makes {@code next} the current phase; a thread already finished ends it at once. */
        synchronized void enter(Span next) {
            if (phase != null) {
                phase.end();
            }
            if (finished) {
                next.end();
                phase = null;
                return;
            }
            phase = next;
        }

        synchronized Span current() {
            return phase == null ? root : phase;
        }

        synchronized void finish(String outcome) {
            finished = true;
            if (phase != null) {
                phase.end();
                phase = null;
            }
            root.setAttribute(OUTCOME, outcome == null ? "" : outcome);
            root.end();
        }
    }

    private final Map<String, Live> live = new ConcurrentHashMap<>();

    /** Opens the thread's {@code discussion} span, a child of the current context (the question's trace). */
    public void opened(String threadId, String crew, String origin) {
        if (threadId == null || threadId.isBlank()) {
            return;
        }
        Span root = AgentTrace.tracer().spanBuilder("discussion").setParent(Context.current())
                .setSpanKind(SpanKind.INTERNAL)
                .setAttribute(THREAD, Attrs.nullToEmpty(threadId))
                .setAttribute(CREW, Attrs.nullToEmpty(crew))
                .setAttribute(ORIGIN, Attrs.nullToEmpty(origin))
                .startSpan();
        Live previous = live.put(threadId, new Live(root));
        if (previous != null) {
            previous.finish("replaced");
        }
    }

    /** Ends the thread's current phase span and starts the next one. */
    public void phase(String threadId, String phase, String reason) {
        Live thread = live.get(threadId);
        if (thread == null) {
            return;
        }
        Span next = AgentTrace.tracer().spanBuilder("phase " + phase)
                .setParent(Context.root().with(thread.root))
                .setAttribute(PHASE, phase)
                .setAttribute(REASON, Attrs.nullToEmpty(reason))
                .startSpan();
        thread.enter(next);
    }

    /** Records who the coordinator convened for the thread. */
    public void convened(String threadId, Collection<String> agents) {
        Live thread = live.get(threadId);
        if (thread == null) {
            return;
        }
        List<String> names = new ArrayList<>(agents);
        java.util.Collections.sort(names);
        thread.root.setAttribute(CONVENED, names);
        thread.root.setAttribute(CONVENED_COUNT, (long) names.size());
        thread.root.addEvent("convened", Attributes.of(CONVENED, names));
    }

    /** Ends the thread's spans with its outcome. */
    public void closed(String threadId, String outcome) {
        Live thread = live.remove(threadId);
        if (thread != null) {
            thread.finish(outcome);
        }
    }

    /**
     * Makes the thread's current phase the current span for the caller, so what the caller
     * publishes or calls hangs under it. Close the scope when done; for an unknown thread it does nothing.
     */
    public Scope inThread(String threadId) {
        Live thread = threadId == null ? null : live.get(threadId);
        if (thread == null) {
            return Scope.noop();
        }
        return Context.current().with(thread.current()).makeCurrent();
    }

    /** How many threads have an open discussion span; for tests. */
    int openCount() {
        return live.size();
    }
}
