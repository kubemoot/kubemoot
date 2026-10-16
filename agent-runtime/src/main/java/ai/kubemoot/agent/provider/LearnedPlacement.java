package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.Placement;
import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;

import java.util.Optional;

/**
 * The learned placement policy: places calls from observed performance instead of
 * a hand-written order.
 *
 * <p>Until it learns from recorded history, it has no opinion of its own:
 * {@link #propose} returns empty and {@link #place} places with the algorithm, so
 * selecting {@code learned} or {@code shadow} is safe from the start.</p>
 */
public class LearnedPlacement implements PlacementPolicy {

    /** Where the learned policy would run a call, and why. Proposing claims nothing. */
    public record Proposal(String model, String provider, String reason) {}

    private final PlacementPolicy fallback;

    /** {@code fallback} places every call the learned policy has no opinion on. */
    public LearnedPlacement(PlacementPolicy fallback) {
        this.fallback = fallback;
    }

    @Override
    public PlacementMode mode() {
        return PlacementMode.LEARNED;
    }

    /** Where this policy would place the call, without claiming anything; empty with no opinion. */
    public Optional<Proposal> propose(PlacementRequest request) {
        return Optional.empty();
    }

    @Override
    public Optional<Placement> place(PlacementRequest request) {
        return fallback.place(request);
    }
}
