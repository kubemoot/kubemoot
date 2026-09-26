package ai.kubemoot.agent.chat;

/**
 * Thrown by {@link ChatService#callWithToolLoop} when bounded tool retries
 * are exhausted — either the same tool failed multiple times in one loop, or
 * the overall tool error rate in this evaluation exceeds the threshold.
 *
 * Signals to the caller (DiscussionSubscriber) that the agent's evaluation
 * could not complete due to a tool/MCP failure, distinct from the agent
 * choosing to stand aside or producing a normal response. The caller is
 * expected to publish a first-class {@code failure} consensus signal with
 * this exception's metadata, so the discussion table — coordinator settle
 * logic, dashboard timeline, gap detection — sees the failure as the
 * structured event it is, instead of silent heartbeats or a synthetic
 * stand_aside that conflates "agent gave up" with "infrastructure broke."
 *
 * Design principle: failure is a first-class signal in the consensus
 * protocol. See tasks/notes/Agent Failure as First-Class Consensus Signal.md
 * and the [[feedback_embrace_failure]] memory.
 */
public class ToolCallFailure extends RuntimeException {

    /**
     * Categorises why the tool loop aborted. Encoded into the failure
     * signal's metadata.failureType so the dashboard and GapDetector can
     * branch on root cause without parsing free-form messages.
     */
    public enum FailureType {
        /** Same tool failed repeatedly (e.g., MCP server unreachable). */
        SAME_TOOL_REPEATED,
        /** Overall tool error rate too high across multiple distinct tools. */
        TOO_MANY_TOOL_FAILURES,
        /** Tool loop ran to maxIterations without producing a final answer. */
        ITERATIONS_EXHAUSTED,
        /**
         * Wall-clock deadline exceeded mid-loop. Distinct from
         * {@link #ITERATIONS_EXHAUSTED}: bounds total elapsed time
         * across successful-but-slow iterations (e.g., agent ground
         * through 5 slow iterations on a cold model for 10 minutes
         * before any per-call timeout fired). Observed concretely
         * 2026-05-26 in discussion dccdb33f. The failure-signal path
         * already surfaces this via metadata.failureType so the
         * coordinator settles quickly and the synthesis is honest.
         */
        LOOP_TIME_EXCEEDED,
        /**
         * A compute-contract agent (KUBEMOOT_DISCUSS_COMPUTE_CONTRACT) answered
         * without ever running execute_code and after re-prompting still would
         * not - so it failed rather than publish an uncomputed (in-head) number.
         */
        COMPUTE_CONTRACT_UNSATISFIED,
        /**
         * A tooler ran its tools but, by its OWN judgment, they errored or returned
         * no usable data, so it declared the gather failed (NO_DATA) rather than post
         * the error text as findings. This is a FAILED GATHER - distinct from a
         * missing tool (which is a TOOL_GAP concern -> onboarding) and from an honest
         * empty result (a real "none" finding the tooler reports as data). It surfaces
         * as a first-class failure signal so the coordinator never synthesizes over
         * laundered error text.
         */
        GATHER_FAILED
    }

    private final FailureType failureType;
    private final String toolName;
    private final String lastErrorContent;
    private final int failureCount;

    public ToolCallFailure(FailureType failureType, String toolName, String lastErrorContent,
                           int failureCount, String message) {
        super(message);
        this.failureType = failureType;
        this.toolName = toolName;
        this.lastErrorContent = lastErrorContent;
        this.failureCount = failureCount;
    }

    public FailureType failureType() { return failureType; }
    public String toolName() { return toolName; }
    public String lastErrorContent() { return lastErrorContent; }
    public int failureCount() { return failureCount; }
}
