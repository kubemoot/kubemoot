package ai.kubemoot.agent.nats;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;

import static org.junit.jupiter.api.Assertions.*;

class DiscussionMetricsTest {

    private MeterRegistry registry;
    private DiscussionMetrics metrics;

    @BeforeEach
    void setUp() {
        registry = new SimpleMeterRegistry();
        metrics = new DiscussionMetrics(registry);
    }

    @Test
    void threadLifecycleCounters() {
        metrics.threadStarted();
        metrics.threadStarted();
        metrics.threadCompleted(Duration.ofSeconds(10));
        metrics.threadTimedOut();
        metrics.threadFastPath(Duration.ofSeconds(3));

        assertEquals(2, counter("kubemoot_discussion_threads_total", "state", "started"));
        assertEquals(1, counter("kubemoot_discussion_threads_total", "state", "completed"));
        assertEquals(1, counter("kubemoot_discussion_threads_total", "state", "timed_out"));
        assertEquals(1, counter("kubemoot_discussion_threads_total", "state", "fast_path"));
    }

    @Test
    void signalCounters() {
        metrics.recordSignals(3, 1, 2, 0);

        assertEquals(3, counter("kubemoot_discussion_signals_total", "type", "agree"));
        assertEquals(1, counter("kubemoot_discussion_signals_total", "type", "concern"));
        assertEquals(2, counter("kubemoot_discussion_signals_total", "type", "stand_aside"));
        assertEquals(0, counter("kubemoot_discussion_signals_total", "type", "block"));
    }

    @Test
    void gapCounters() {
        metrics.toolGap();
        metrics.toolGap();
        metrics.toolerGap();

        assertEquals(2, counter("kubemoot_discussion_gaps_total", "type", "tool"));
        assertEquals(1, counter("kubemoot_discussion_gaps_total", "type", "tooler"));
    }

    @Test
    void phaseDurations() {
        metrics.advisoryCompleted(Duration.ofMillis(1500));
        metrics.evaluationCompleted(Duration.ofSeconds(8));
        metrics.synthesisCompleted(Duration.ofSeconds(3));

        assertEquals(1, timer("kubemoot_discussion_phase_duration_seconds", "phase", "advisory"));
        assertEquals(1, timer("kubemoot_discussion_phase_duration_seconds", "phase", "evaluation"));
        assertEquals(1, timer("kubemoot_discussion_phase_duration_seconds", "phase", "synthesis"));
    }

    @Test
    void threadDurationRecorded() {
        metrics.threadCompleted(Duration.ofSeconds(15));
        metrics.threadFastPath(Duration.ofSeconds(5));

        assertEquals(2, registry.find("kubemoot_discussion_thread_duration_seconds")
                .timer().count());
    }

    @Test
    void tokenCounters() {
        metrics.recordTokens(500, 200);
        metrics.recordTokens(300, 100);

        assertEquals(800, counter("kubemoot_discussion_tokens_total", "direction", "input"));
        assertEquals(300, counter("kubemoot_discussion_tokens_total", "direction", "output"));
    }

    @Test
    void agentInferenceTiming() {
        metrics.recordAgentInference(Duration.ofMillis(2500));
        metrics.recordAgentInference(Duration.ofMillis(1500));

        assertEquals(2, registry.find("kubemoot_discussion_agent_inference_seconds")
                .timer().count());
    }

    @Test
    void participantDistribution() {
        metrics.recordSignals(3, 1, 2, 0);  // 6 participants
        metrics.recordSignals(1, 0, 0, 0);  // 1 participant

        var summary = registry.find("kubemoot_discussion_participants").summary();
        assertNotNull(summary);
        assertEquals(2, summary.count());
    }

    @Test
    void evaluatingSignalCounter() {
        metrics.evaluatingSignalReceived();
        metrics.evaluatingSignalReceived();
        metrics.evaluatingSignalReceived();

        assertEquals(3, counter("kubemoot_discussion_signals_total", "type", "evaluating"));
    }

    @Test
    void evaluationTimeoutCounter() {
        metrics.evaluationTimedOut();
        metrics.evaluationTimedOut();

        var c = registry.find("kubemoot_discussion_evaluation_timeouts_total").counter();
        assertNotNull(c);
        assertEquals(2, c.count(), 0.001);
    }

    @Test
    void pendingEvaluationsGauge() {
        metrics.setPendingEvaluations(5);

        var gauge = registry.find("kubemoot_discussion_pending_evaluations").gauge();
        assertNotNull(gauge);
        assertEquals(5.0, gauge.value(), 0.001);

        metrics.setPendingEvaluations(2);
        assertEquals(2.0, gauge.value(), 0.001);
    }

    private double counter(String name, String tagKey, String tagValue) {
        var c = registry.find(name).tag(tagKey, tagValue).counter();
        return c != null ? c.count() : 0;
    }

    private long timer(String name, String tagKey, String tagValue) {
        var t = registry.find(name).tag(tagKey, tagValue).timer();
        return t != null ? t.count() : 0;
    }
}
