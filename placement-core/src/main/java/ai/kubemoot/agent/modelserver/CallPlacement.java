package ai.kubemoot.agent.modelserver;

import java.util.List;
import java.util.Optional;
import java.util.function.Supplier;

/**
 * What a caller needs from placement: where a call runs, the plan made for a thread when its agent
 * was selected, the demand a caller adds to the shared view, and the bookkeeping that follows a call
 * (giving the ticket back, recording residency and the outcome).
 *
 * <p>{@link CallPlanner} implements it in-process over the NATS KV buckets every agent shares. The
 * agent's remote implementation asks the placement service, which owns the tickets and holds the
 * thread-scoped plans, so the same caller code runs against either.</p>
 */
public interface CallPlacement {

    /** The placement mode in use. */
    PlacementMode mode();

    /**
     * One placement attempt; empty when no server has room and waiting is the better choice. The
     * placement carries its {@link PlacementDecision}. The caller gives the claimed ticket back
     * with {@link #releaseTicket}.
     *
     * @throws PlacementUnavailableException when a remote placement cannot be reached
     */
    Optional<CallPlanner.Placement> place(CallPlanner.PlacementRequest request);

    /**
     * The agent was selected for {@code threadId}: publish its intent for {@code candidates}, record
     * the selection for prediction, and plan its first call now with the request {@code request}
     * builds (empty when there is nothing to plan). A plan with a claim is held for the first call.
     */
    void commitPlan(String threadId, long selectedAtMs, List<String> candidates,
                    Supplier<Optional<CallPlanner.PlacementRequest>> request);

    /** The plan held for this thread's first call, handed over once; the caller owns its ticket. */
    Optional<CallPlanner.Placement> takeHeld(String threadId);

    /** The agent stood aside or the thread ended: release any held claim and clear its intents. */
    void release(String threadId);

    /** A call started on {@code model}: the agent's intents are fulfilled, and the use is recorded. */
    void callStarted(String threadId, String model);

    /** This agent started waiting for an accelerator for {@code model}. */
    void waitStarted(String model);

    /** This agent's wait for {@code model} ended. */
    void waitEnded(String model);

    /** Gives a claimed ticket back. Idempotent and best-effort. */
    void releaseTicket(Ticket ticket);

    /** Records that {@code model} is resident on {@code server} with the footprint, so other callers see it at once. */
    void recordResidency(String server, String model, long footprintMiB);

    /** The calls in flight on {@code server} right now. */
    int activeCountFor(String server);

    /**
     * A finished call: its time on {@code server} and whether it succeeded, for ranking and the
     * circuit breaker. A {@code model} of null leaves the per-model outcome history alone.
     */
    void recordOutcome(String server, String model, long durationMs, boolean ok);

    /** The server failed the caller before a call could start (for example the model would not wake). */
    void recordFailure(String server);

    /**
     * The server this placement last chose for {@code model}, for a caller that must place a call
     * without it (see {@link PlacementUnavailableException}). Empty when none is known, and always
     * empty for in-process placement, which is never unavailable.
     */
    default Optional<String> lastKnownServer(String model) {
        return Optional.empty();
    }
}
