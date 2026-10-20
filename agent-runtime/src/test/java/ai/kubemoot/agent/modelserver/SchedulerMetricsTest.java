package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.nats.DiscussionMetrics;
import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

class SchedulerMetricsTest {

    private static CallObservation call(String kind, boolean ok, boolean cold, long outputTokens, long evalMs) {
        return new CallObservation("ollama-gpu", "qwen3:8b", kind, ok, cold, 100, outputTokens, 1_500,
                cold ? 2_900 : CallObservation.UNKNOWN, 40, evalMs, 1, 0L);
    }

    @Test
    void aCallIsCountedByPolicyServerModelKindStartAndOutcome() {
        var registry = new SimpleMeterRegistry();
        var metrics = new SchedulerMetrics(registry, "learned");
        metrics.call(call(CallObservation.KIND_TRIAGE, true, true, 256, 1_000));
        metrics.call(call(CallObservation.KIND_TRIAGE, false, false, 0, CallObservation.UNKNOWN));

        var cold = registry.find(SchedulerMetrics.CALLS).tags("policy", "learned", "server", "ollama-gpu",
                "model", "qwen3:8b", "kind", "triage", "start", "cold", "outcome", "ok").counter();
        assertNotNull(cold);
        assertEquals(1.0, cold.count());
        assertNotNull(registry.find(SchedulerMetrics.CALLS).tags("start", "warm", "outcome", "failed").counter());
        assertEquals(2, registry.find(SchedulerMetrics.CALL_SECONDS).timer().count());
        assertEquals(256.0, registry.find(SchedulerMetrics.GENERATED_TOKENS).counter().count(),
                "only successful calls with engine timings count toward generation speed");
        assertEquals(1.0, registry.find(SchedulerMetrics.GENERATE_SECONDS).counter().count());
    }

    @Test
    void waitsAreTimedByTheServerAndModelPlaced() {
        var registry = new SimpleMeterRegistry();
        var metrics = new SchedulerMetrics(registry, (String) null);
        metrics.waited("ollama-gpu", "qwen3:8b", 4_000);
        metrics.waited("", "qwen3:8b", 1_000);
        metrics.waited("ollama-gpu", "qwen3:8b", -1);
        var timer = registry.find(SchedulerMetrics.WAIT_SECONDS).timer();
        assertEquals(1, timer.count());
        assertEquals(4.0, timer.totalTime(java.util.concurrent.TimeUnit.SECONDS));
    }

    @Test
    void unattributableCallsAreIgnored() {
        var registry = new SimpleMeterRegistry();
        new SchedulerMetrics(registry, "algorithm").call(new CallObservation("", "", "simple", true, false,
                0, 0, 1, -1, -1, -1, 0, 0L));
        assertTrue(registry.getMeters().isEmpty());
        new SchedulerMetrics(registry, "algorithm").call(null);
        SchedulerMetrics.none().call(call("simple", true, false, 1, 1));
        assertTrue(registry.getMeters().isEmpty());
    }

    /** Every Kubemoot metric Prometheus scrapes has a description: a HELP line with text. */
    @Test
    void everyKubemootMetricHasADescription() {
        var registry = new io.micrometer.prometheus.PrometheusMeterRegistry(
                io.micrometer.prometheus.PrometheusConfig.DEFAULT);
        var scheduler = new SchedulerMetrics(registry, "shadow");
        scheduler.call(call(CallObservation.KIND_TOOL_TURN, true, false, 10, 100));
        scheduler.waited("p", "m", 10);
        scheduler.waitedUnplaced("m", 10);
        PlacementPolicies.shadowRecorder(registry).accept(new ShadowPlacement.Decision("m", null, null));
        var discussion = new DiscussionMetrics(registry);
        discussion.threadStarted();
        discussion.threadCompleted(Duration.ofSeconds(1));
        discussion.recordSignals(1, 1, 1, 1);
        discussion.toolGap();
        discussion.recordTokens(1, 1);
        discussion.recordCoordinatorTokens(1, 1);
        discussion.triageCompleted(Duration.ofSeconds(1), 2);
        String scrape = registry.scrape();

        List<String> types = scrape.lines().filter(l -> l.startsWith("# TYPE kubemoot_"))
                .map(l -> l.split(" ")[2]).toList();
        List<String> undescribed = types.stream()
                .filter(name -> scrape.lines().noneMatch(l -> l.startsWith("# HELP " + name + " ")
                        && l.length() > ("# HELP " + name + " ").length()))
                .toList();
        assertTrue(undescribed.isEmpty(), "metrics without a description: " + undescribed);
        assertTrue(types.size() > 10, "metrics scraped: " + types);
    }

    /** What Prometheus scrapes: the exported names, a HELP line for each, and buckets for the timers. */
    @Test
    void prometheusExportsEveryNameWithItsHelpAndBuckets() {
        var registry = new io.micrometer.prometheus.PrometheusMeterRegistry(
                io.micrometer.prometheus.PrometheusConfig.DEFAULT);
        var metrics = new SchedulerMetrics(registry, "shadow");
        metrics.call(call(CallObservation.KIND_TOOL_TURN, true, false, 10, 100));
        metrics.call(call(CallObservation.KIND_TRIAGE, false, true, 0, CallObservation.UNKNOWN));
        metrics.waited("ollama-gpu", "qwen3:8b", 3_000);
        metrics.waitedUnplaced("qwen3:8b", 60_000);
        PlacementPolicies.shadowRecorder(registry).accept(new ShadowPlacement.Decision("m", null, null));
        var discussion = new DiscussionMetrics(registry);
        discussion.threadCompleted(Duration.ofSeconds(40));
        discussion.triageCompleted(Duration.ofSeconds(3), 2);
        String scrape = registry.scrape();

        for (String series : List.of("kubemoot_scheduler_calls_total", "kubemoot_scheduler_call_seconds_count",
                "kubemoot_scheduler_call_seconds_sum", "kubemoot_scheduler_call_seconds_bucket",
                "kubemoot_scheduler_generated_tokens_total", "kubemoot_scheduler_generate_seconds_total",
                "kubemoot_scheduler_capacity_wait_seconds_bucket", "kubemoot_scheduler_shadow_decisions_total",
                "kubemoot_discussion_thread_duration_seconds_bucket", "kubemoot_discussion_phase_duration_seconds_bucket")) {
            assertTrue(scrape.contains("\n" + series + "{"), "missing " + series);
        }
        for (String name : List.of("kubemoot_scheduler_calls_total", "kubemoot_scheduler_call_seconds",
                "kubemoot_scheduler_capacity_wait_seconds", "kubemoot_discussion_phase_duration_seconds")) {
            assertTrue(scrape.contains("# HELP " + name + " ") && !scrape.contains("# HELP " + name + " \n"),
                    "no HELP text for " + name);
        }
        assertTrue(scrape.contains("# HELP kubemoot_discussion_phase_duration_seconds Discussion phase duration, by phase"),
                "the phases share one description");
    }
}
