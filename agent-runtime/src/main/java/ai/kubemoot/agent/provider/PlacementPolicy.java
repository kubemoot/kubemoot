package ai.kubemoot.agent.provider;

import java.util.Optional;

/**
 * Decides where a call runs: which model, on which provider, or that the call waits.
 *
 * <p>This is the swappable part of the scheduler. Tickets, slot counting, and
 * residency stay in {@link TicketManager}, which every policy shares, so two
 * policies never overbook the same accelerator. {@link CallPlanner} holds one
 * policy, chosen by {@link PlacementMode}.</p>
 */
public interface PlacementPolicy {

    /** The mode this policy implements. */
    PlacementMode mode();

    /**
     * Places the call and claims its ticket; empty when no provider has room and
     * waiting is the better choice. The caller releases the claimed ticket.
     */
    Optional<CallPlanner.Placement> place(CallPlanner.PlacementRequest request);
}
