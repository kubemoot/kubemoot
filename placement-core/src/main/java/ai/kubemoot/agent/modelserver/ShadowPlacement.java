package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import ai.kubemoot.agent.modelserver.LearnedPlacement.Proposal;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.Optional;
import java.util.function.Consumer;

/**
 * Shadow mode: the algorithm places every call, and the learned policy's choice for
 * the same call is recorded beside it. Placement never depends on the learned policy.
 */
public class ShadowPlacement implements PlacementPolicy {

    private static final Logger log = LoggerFactory.getLogger(ShadowPlacement.class);

    /** How the two choices compared for one call. */
    public enum Agreement { AGREE, DIFFER, NO_OPINION }

    /**
     * One call's actual placement beside the learned policy's proposal. {@code actual} is
     * null when the algorithm chose to wait; {@code learned} is null with no opinion.
     */
    public record Decision(String preferred, Placement actual, Proposal learned) {
        /** AGREE when both chose the same model on the same server. */
        public Agreement agreement() {
            if (learned == null) {
                return Agreement.NO_OPINION;
            }
            if (actual == null) {
                return Agreement.DIFFER;
            }
            boolean same = actual.model().equals(learned.model())
                    && actual.pick().server().name().equals(learned.server());
            return same ? Agreement.AGREE : Agreement.DIFFER;
        }
    }

    private final PlacementPolicy algorithm;
    private final LearnedPlacement learned;
    private final Consumer<Decision> recorder;

    public ShadowPlacement(PlacementPolicy algorithm, LearnedPlacement learned, Consumer<Decision> recorder) {
        this.algorithm = algorithm;
        this.learned = learned;
        this.recorder = recorder;
    }

    @Override
    public PlacementMode mode() {
        return PlacementMode.SHADOW;
    }

    @Override
    public Optional<Placement> place(PlacementRequest request) {
        Optional<Placement> actual = algorithm.place(request);
        Optional<Proposal> proposal = proposeSafely(request);
        try {
            recorder.accept(new Decision(request.preferred(), actual.orElse(null), proposal.orElse(null)));
        } catch (RuntimeException e) {
            log.warn("Shadow placement record failed: {}", e.getMessage());
        }
        return actual;
    }

    /** The learned policy's proposal; a failure there never affects placement. */
    private Optional<Proposal> proposeSafely(PlacementRequest request) {
        try {
            return learned.propose(request);
        } catch (RuntimeException e) {
            log.warn("Learned policy failed to propose for {}: {}", request.preferred(), e.getMessage());
            return Optional.empty();
        }
    }
}
