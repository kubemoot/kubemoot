package ai.kubemoot.agent.nats;

import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.DistributionSummary;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import jakarta.enterprise.context.ApplicationScoped;

import java.time.Duration;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Prometheus metrics for the consensus discussion system.
 *
 * Exposed at /q/metrics. All metrics are prefixed with "kubemoot_discussion_".
 */
@ApplicationScoped
public class DiscussionMetrics {

    // Metric name constants
    private static final String METRIC_THREADS_TOTAL = "kubemoot_discussion_threads_total";
    private static final String METRIC_SIGNALS_TOTAL = "kubemoot_discussion_signals_total";
    private static final String METRIC_GAPS_TOTAL = "kubemoot_discussion_gaps_total";
    private static final String METRIC_PHASE_DURATION = "kubemoot_discussion_phase_duration_seconds";
    private static final String METRIC_TOKENS_TOTAL = "kubemoot_discussion_tokens_total";
    private static final String METRIC_COORDINATOR_TOKENS = "kubemoot_discussion_coordinator_tokens_total";

    // Tag key constants
    private static final String TAG_TYPE = "type";
    private static final String TAG_DIRECTION = "direction";
    private static final String TAG_STATE = "state";
    private static final String TAG_PHASE = "phase";

    private final MeterRegistry registry;

    // Thread lifecycle
    private final Counter threadsStarted;
    private final Counter threadsCompleted;
    private final Counter threadsTimedOut;
    private final Counter threadsFastPath;

    // Signals
    private final Counter signalsAgree;
    private final Counter signalsConcern;
    private final Counter signalsStandAside;
    private final Counter signalsBlock;

    // Gaps
    private final Counter gapsTooler;
    private final Counter gapsTool;

    // Durations
    private final Timer threadDuration;
    private final Timer advisoryDuration;
    private final Timer evaluationDuration;
    private final Timer synthesisDuration;

    // Participation
    private final DistributionSummary participantCount;

    // Tokens (from tooler responses published via NATS metadata)
    private final Counter tokensInput;
    private final Counter tokensOutput;

    // Inference timing for tooler responses
    private final Timer agentInference;

    // Triage phase
    private final Timer triageDuration;
    private final Counter coordinatorTokensInput;
    private final Counter coordinatorTokensOutput;
    private final DistributionSummary triageAgentsSelected;

    // Evaluating signal tracking
    private final Counter signalsEvaluating;
    private final Counter evaluationTimeouts;
    private final AtomicInteger pendingEvaluationsGauge = new AtomicInteger(0);

    public DiscussionMetrics(MeterRegistry registry) {
        this.registry = registry;

        threadsStarted = Counter.builder(METRIC_THREADS_TOTAL)
                .tag(TAG_STATE, "started")
                .description("Discussion threads started")
                .register(registry);
        threadsCompleted = Counter.builder(METRIC_THREADS_TOTAL)
                .tag(TAG_STATE, "completed")
                .description("Discussion threads completed with synthesis")
                .register(registry);
        threadsTimedOut = Counter.builder(METRIC_THREADS_TOTAL)
                .tag(TAG_STATE, "timed_out")
                .description("Discussion threads that timed out")
                .register(registry);
        threadsFastPath = Counter.builder(METRIC_THREADS_TOTAL)
                .tag(TAG_STATE, "fast_path")
                .description("Discussion threads resolved via single-agree fast path")
                .register(registry);

        signalsAgree = counter(METRIC_SIGNALS_TOTAL, TAG_TYPE, "agree");
        signalsConcern = counter(METRIC_SIGNALS_TOTAL, TAG_TYPE, "concern");
        signalsStandAside = counter(METRIC_SIGNALS_TOTAL, TAG_TYPE, "stand_aside");
        signalsBlock = counter(METRIC_SIGNALS_TOTAL, TAG_TYPE, "block");

        gapsTooler = counter(METRIC_GAPS_TOTAL, TAG_TYPE, "tooler");
        gapsTool = counter(METRIC_GAPS_TOTAL, TAG_TYPE, "tool");

        threadDuration = timer("kubemoot_discussion_thread_duration_seconds", "Total thread duration");
        advisoryDuration = timer(METRIC_PHASE_DURATION, "Advisory phase duration", TAG_PHASE, "advisory");
        evaluationDuration = timer(METRIC_PHASE_DURATION, "Evaluation phase duration", TAG_PHASE, "evaluation");
        synthesisDuration = timer(METRIC_PHASE_DURATION, "Synthesis phase duration", TAG_PHASE, "synthesis");

        participantCount = DistributionSummary.builder("kubemoot_discussion_participants")
                .description("Number of participating agents per thread")
                .register(registry);

        tokensInput = counter(METRIC_TOKENS_TOTAL, TAG_DIRECTION, "input");
        tokensOutput = counter(METRIC_TOKENS_TOTAL, TAG_DIRECTION, "output");

        agentInference = Timer.builder("kubemoot_discussion_agent_inference_seconds")
                .description("Agent inference duration during discussions")
                .register(registry);

        triageDuration = timer(METRIC_PHASE_DURATION, "Triage phase duration", TAG_PHASE, "triage");
        coordinatorTokensInput = counter(METRIC_COORDINATOR_TOKENS, TAG_DIRECTION, "input");
        coordinatorTokensOutput = counter(METRIC_COORDINATOR_TOKENS, TAG_DIRECTION, "output");
        triageAgentsSelected = DistributionSummary.builder("kubemoot_discussion_triage_agents_selected")
                .description("Number of agents selected by triage per thread")
                .register(registry);

        signalsEvaluating = counter(METRIC_SIGNALS_TOTAL, TAG_TYPE, "evaluating");
        evaluationTimeouts = Counter.builder("kubemoot_discussion_evaluation_timeouts_total")
                .description("Agents that timed out in pending evaluation (synthesized stand_aside)")
                .register(registry);
        Gauge.builder("kubemoot_discussion_pending_evaluations", pendingEvaluationsGauge, AtomicInteger::get)
                .description("Current number of agents with an active evaluating signal across all threads")
                .register(registry);
    }

    // --- Thread lifecycle ---

    public void threadStarted() {
        threadsStarted.increment();
    }

    public void threadCompleted(Duration duration) {
        threadsCompleted.increment();
        threadDuration.record(duration);
    }

    public void threadTimedOut() {
        threadsTimedOut.increment();
    }

    public void threadFastPath(Duration duration) {
        threadsFastPath.increment();
        threadDuration.record(duration);
    }

    // --- Phase durations ---

    public void advisoryCompleted(Duration duration) {
        advisoryDuration.record(duration);
    }

    public void evaluationCompleted(Duration duration) {
        evaluationDuration.record(duration);
    }

    public void synthesisCompleted(Duration duration) {
        synthesisDuration.record(duration);
    }

    // --- Signals ---

    public void recordSignals(int agrees, int concerns, int standAsides, int blocks) {
        signalsAgree.increment(agrees);
        signalsConcern.increment(concerns);
        signalsStandAside.increment(standAsides);
        signalsBlock.increment(blocks);
        participantCount.record(agrees + concerns + standAsides + blocks);
    }

    // --- Gaps ---

    public void toolerGap() {
        gapsTooler.increment();
    }

    public void toolGap() {
        gapsTool.increment();
    }

    // --- Tokens (from tooler agent responses) ---

    public void recordTokens(long inputTokens, long outputTokens) {
        tokensInput.increment(inputTokens);
        tokensOutput.increment(outputTokens);
    }

    // --- Agent inference ---

    public void recordAgentInference(Duration duration) {
        agentInference.record(duration);
    }

    // --- Triage ---

    public void triageCompleted(Duration duration, int agentsSelected) {
        triageDuration.record(duration);
        triageAgentsSelected.record(agentsSelected);
    }

    public void recordCoordinatorTokens(long inputTokens, long outputTokens) {
        coordinatorTokensInput.increment(inputTokens);
        coordinatorTokensOutput.increment(outputTokens);
    }

    // --- Evaluating signal ---

    public void evaluatingSignalReceived() {
        signalsEvaluating.increment();
    }

    public void evaluationTimedOut() {
        evaluationTimeouts.increment();
    }

    public void setPendingEvaluations(int count) {
        pendingEvaluationsGauge.set(count);
    }

    // --- Helpers ---

    private Counter counter(String name, String tagKey, String tagValue) {
        return Counter.builder(name).tag(tagKey, tagValue).register(registry);
    }

    private Timer timer(String name, String description) {
        return Timer.builder(name).description(description).register(registry);
    }

    private Timer timer(String name, String description, String tagKey, String tagValue) {
        return Timer.builder(name).description(description).tag(tagKey, tagValue).register(registry);
    }
}
