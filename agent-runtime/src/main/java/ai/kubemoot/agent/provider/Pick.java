package ai.kubemoot.agent.provider;

/**
 * Result of a successful JIT provider selection + ticket claim.
 *
 * Pairs the chosen {@link ProviderState} (where the call will run) with
 * the {@link Ticket} that reserved its VRAM footprint. The caller MUST
 * release the ticket in a {@code finally} block on call completion so
 * the next selection sees the freed budget — see {@code TicketManager}
 * javadoc for the two-layer release guarantee.
 *
 * Returned by {@link ProviderSelector#pickAndClaim}. When pick or claim
 * fails (no candidate fits, NATS unavailable, all candidates lose the
 * race), {@code pickAndClaim} returns {@link java.util.Optional#empty}
 * and the caller falls back to its static Quarkus-injected ChatModel.
 *
 * {@code queued} is true when the claim joined a copy (resident or loading)
 * without verifying a free call slot after its ticket was written: the call may
 * wait behind the copy's other calls.
 *
 * See {@code kubemoot/docs/scheduler.md} — "JIT GPU Scheduling →
 * Update (2026-05-28): VRAM-headroom tickets".
 */
public record Pick(ProviderState provider, Ticket ticket, FitScore score, boolean queued) {
    /** A pick that holds a counted call slot or starts its own load. */
    public Pick(ProviderState provider, Ticket ticket, FitScore score) {
        this(provider, ticket, score, false);
    }

    /** Backwards-compatible constructor for callers that don't surface a score (tests, fallback paths). */
    public Pick(ProviderState provider, Ticket ticket) {
        this(provider, ticket, FitScore.yes(0L, "no score"));
    }
}
