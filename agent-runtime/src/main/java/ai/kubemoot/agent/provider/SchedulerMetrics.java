package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.config.AgentProperties;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import io.micrometer.core.instrument.composite.CompositeMeterRegistry;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import java.time.Duration;

/**
 * The scheduler's metrics: every model call this agent made and every wait for
 * capacity, by placement policy, provider, and model. Each metric carries a
 * description, which Prometheus serves as its HELP text. The timers publish
 * histogram buckets, so percentiles can be computed across agents.
 */
@ApplicationScoped
public class SchedulerMetrics {

    static final String CALLS = "kubemoot_scheduler_calls";
    static final String CALL_SECONDS = "kubemoot_scheduler_call_seconds";
    static final String GENERATED_TOKENS = "kubemoot_scheduler_generated_tokens";
    static final String GENERATE_SECONDS = "kubemoot_scheduler_generate_seconds";
    static final String WAIT_SECONDS = "kubemoot_scheduler_capacity_wait_seconds";

    /** Bucket bounds for the call and wait timers: model calls take from a second to minutes. */
    static final Duration[] BUCKETS = {
            Duration.ofSeconds(1), Duration.ofSeconds(2), Duration.ofSeconds(5), Duration.ofSeconds(10),
            Duration.ofSeconds(20), Duration.ofSeconds(30), Duration.ofSeconds(60), Duration.ofSeconds(120),
            Duration.ofSeconds(300), Duration.ofSeconds(600)};

    private static final String PROVIDER = "provider";
    private static final String MODEL = "model";

    private final MeterRegistry registry;
    private final String policy;

    @Inject
    public SchedulerMetrics(MeterRegistry registry, AgentProperties properties) {
        this(registry, properties.scheduler().placementPolicy());
    }

    SchedulerMetrics(MeterRegistry registry, String policy) {
        this.registry = registry;
        this.policy = policy == null || policy.isBlank() ? "algorithm" : policy;
    }

    /** Metrics that go nowhere, for a history built without a registry. */
    public static SchedulerMetrics none() {
        return new SchedulerMetrics(new CompositeMeterRegistry(), "algorithm");
    }

    /** Records one model call; ignored when it names no provider or model. */
    public void call(CallObservation o) {
        if (o == null || !o.attributable()) {
            return;
        }
        String kind = o.kind() == null ? "" : o.kind();
        countCall(o, kind);
        if (o.durationMs() >= 0) {
            timeCall(o, kind);
        }
        if (o.ok() && o.outputTokens() > 0 && o.evalMs() > 0) {
            countGeneration(o);
        }
    }

    private void countCall(CallObservation o, String kind) {
        Counter.builder(CALLS)
                .description("Model calls this agent made, by placement policy, provider, model, call kind "
                        + "(tool-turn, simple, triage), whether the model had to load, and outcome")
                .tag("policy", policy).tag(PROVIDER, o.provider()).tag(MODEL, o.model()).tag("kind", kind)
                .tag("start", o.coldStart() ? "cold" : "warm").tag("outcome", o.ok() ? "ok" : "failed")
                .register(registry).increment();
    }

    private void timeCall(CallObservation o, String kind) {
        Timer.builder(CALL_SECONDS)
                .description("Duration of model calls, from sending the request to the reply or failure, "
                        + "by provider, model, and call kind")
                .tag(PROVIDER, o.provider()).tag(MODEL, o.model()).tag("kind", kind)
                .serviceLevelObjectives(BUCKETS)
                .register(registry).record(Duration.ofMillis(o.durationMs()));
    }

    private void countGeneration(CallObservation o) {
        Counter.builder(GENERATED_TOKENS)
                .description("Tokens the engine generated for this agent's successful calls, by provider and model")
                .tag(PROVIDER, o.provider()).tag(MODEL, o.model())
                .register(registry).increment(o.outputTokens());
        Counter.builder(GENERATE_SECONDS)
                .description("Generation time the engine reported for those tokens, by provider and model; "
                        + "generated tokens over it is the generation speed")
                .tag(PROVIDER, o.provider()).tag(MODEL, o.model())
                .register(registry).increment(o.evalMs() / 1000.0);
    }

    /** Records that a call waited {@code waitMs} for capacity and was then placed on {@code provider}. */
    public void waited(String provider, String model, long waitMs) {
        if (waitMs >= 0 && CallObservation.names(provider, model)) {
            timeWait(provider, model, "placed", waitMs);
        }
    }

    /** Records that a call waited {@code waitMs} for capacity for {@code model} and was not placed. */
    public void waitedUnplaced(String model, long waitMs) {
        if (waitMs >= 0 && model != null && !model.isBlank()) {
            timeWait("", model, "not_placed", waitMs);
        }
    }

    private void timeWait(String provider, String model, String outcome, long waitMs) {
        Timer.builder(WAIT_SECONDS)
                .description("Time a call waited for an accelerator with room, by outcome: placed (with the "
                        + "provider and model it was placed on) or not_placed (the thread ended or nothing could take it)")
                .tag(PROVIDER, provider).tag(MODEL, model).tag("outcome", outcome)
                .serviceLevelObjectives(BUCKETS)
                .register(registry).record(Duration.ofMillis(waitMs));
    }
}
