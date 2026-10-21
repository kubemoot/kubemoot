package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.config.AgentProperties;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.DistributionSummary;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import io.micrometer.core.instrument.composite.CompositeMeterRegistry;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;

import java.time.Duration;

/**
 * The scheduler's metrics: every model call this agent made and every wait for
 * capacity, by placement policy, server, and model, and how far each placement's
 * predicted completion time was from the call it placed. Each metric carries a
 * description, which Prometheus serves as its HELP text. The timers and summaries
 * publish histogram buckets, so percentiles can be computed across agents.
 */
@ApplicationScoped
public class SchedulerMetrics {

    static final String CALLS = "kubemoot_scheduler_calls";
    static final String CALL_SECONDS = "kubemoot_scheduler_call_seconds";
    static final String GENERATED_TOKENS = "kubemoot_scheduler_generated_tokens";
    static final String GENERATE_SECONDS = "kubemoot_scheduler_generate_seconds";
    static final String WAIT_SECONDS = "kubemoot_scheduler_capacity_wait_seconds";
    static final String PREDICTION_ERROR_SECONDS = "kubemoot_scheduler_prediction_error_seconds";
    static final String PREDICTION_RELATIVE_ERROR = "kubemoot_scheduler_prediction_relative_error";

    /** Bucket bounds, in seconds, for the absolute prediction error. */
    static final double[] ERROR_SECONDS_BUCKETS = {0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300};
    /** Bucket bounds for the relative prediction error: the error over the measured time. */
    static final double[] RELATIVE_ERROR_BUCKETS = {0.05, 0.1, 0.2, 0.35, 0.5, 0.75, 1, 2, 5, 10};
    /** The two times a prediction is scored on. */
    static final String PART_TOTAL = "total";
    static final String PART_FIRST_TOKEN = "first_token";

    /** Bucket bounds for the call and wait timers: model calls take from a second to minutes. */
    static final Duration[] BUCKETS = {
            Duration.ofSeconds(1), Duration.ofSeconds(2), Duration.ofSeconds(5), Duration.ofSeconds(10),
            Duration.ofSeconds(20), Duration.ofSeconds(30), Duration.ofSeconds(60), Duration.ofSeconds(120),
            Duration.ofSeconds(300), Duration.ofSeconds(600)};

    private static final String POLICY = "policy";
    private static final String SERVER = "server";
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

    /** Records one model call; ignored when it names no server or model. */
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
                .description("Model calls this agent made, by placement policy, server, model, call kind "
                        + "(tool-turn, simple, triage), whether the model had to load, and outcome")
                .tag(POLICY, policy).tag(SERVER, o.server()).tag(MODEL, o.model()).tag("kind", kind)
                .tag("start", o.coldStart() ? "cold" : "warm").tag("outcome", o.ok() ? "ok" : "failed")
                .register(registry).increment();
    }

    private void timeCall(CallObservation o, String kind) {
        Timer.builder(CALL_SECONDS)
                .description("Duration of model calls, from sending the request to the reply or failure, "
                        + "by server, model, and call kind")
                .tag(SERVER, o.server()).tag(MODEL, o.model()).tag("kind", kind)
                .serviceLevelObjectives(BUCKETS)
                .register(registry).record(Duration.ofMillis(o.durationMs()));
    }

    private void countGeneration(CallObservation o) {
        Counter.builder(GENERATED_TOKENS)
                .description("Tokens the engine generated for this agent's successful calls, by server and model")
                .tag(SERVER, o.server()).tag(MODEL, o.model())
                .register(registry).increment(o.outputTokens());
        Counter.builder(GENERATE_SECONDS)
                .description("Generation time the engine reported for those tokens, by server and model; "
                        + "generated tokens over it is the generation speed")
                .tag(SERVER, o.server()).tag(MODEL, o.model())
                .register(registry).increment(o.evalMs() / 1000.0);
    }

    /**
     * Records how far a placement's prediction was from the first request of the call
     * it placed: the absolute and the relative error of the completion time, and, for a
     * streamed request, of the time to the first token. The policy is the one that
     * placed this call, so the error of the learned policy's predictions is comparable
     * with the same predictions under the algorithm.
     */
    public void predictionError(PlacedCall.Score s) {
        if (s == null || !CallObservation.names(s.server(), s.model())) {
            return;
        }
        recordError(s, PART_TOTAL, s.totalAbsMs(), s.totalRelative());
        if (s.hasFirstToken()) {
            recordError(s, PART_FIRST_TOKEN, s.firstTokenAbsMs(), s.firstTokenRelative());
        }
    }

    private void recordError(PlacedCall.Score s, String part, long absMs, double relative) {
        DistributionSummary.builder(PREDICTION_ERROR_SECONDS)
                .description("Absolute error of the predicted completion time against the first request of the "
                        + "call it placed, by the policy that placed the call, server, model, and part "
                        + "(total: the whole request; first_token: the time to the first streamed token)")
                .baseUnit("seconds")
                .tag(POLICY, s.policy()).tag(SERVER, s.server()).tag(MODEL, s.model()).tag("part", part)
                .serviceLevelObjectives(ERROR_SECONDS_BUCKETS)
                .register(registry).record(absMs / 1000.0);
        DistributionSummary.builder(PREDICTION_RELATIVE_ERROR)
                .description("Relative error of the predicted completion time: the absolute error over the "
                        + "measured time, by the policy that placed the call, server, model, and part")
                .tag(POLICY, s.policy()).tag(SERVER, s.server()).tag(MODEL, s.model()).tag("part", part)
                .serviceLevelObjectives(RELATIVE_ERROR_BUCKETS)
                .register(registry).record(relative);
    }

    /** Records that a call waited {@code waitMs} for capacity and was then placed on {@code server}. */
    public void waited(String server, String model, long waitMs) {
        if (waitMs >= 0 && CallObservation.names(server, model)) {
            timeWait(server, model, "placed", waitMs);
        }
    }

    /** Records that a call waited {@code waitMs} for capacity for {@code model} and was not placed. */
    public void waitedUnplaced(String model, long waitMs) {
        if (waitMs >= 0 && model != null && !model.isBlank()) {
            timeWait("", model, "not_placed", waitMs);
        }
    }

    private void timeWait(String server, String model, String outcome, long waitMs) {
        Timer.builder(WAIT_SECONDS)
                .description("Time a call waited for an accelerator with room, by outcome: placed (with the "
                        + "server and model it was placed on) or not_placed (the thread ended or nothing could take it)")
                .tag(SERVER, server).tag(MODEL, model).tag("outcome", outcome)
                .serviceLevelObjectives(BUCKETS)
                .register(registry).record(Duration.ofMillis(waitMs));
    }
}
