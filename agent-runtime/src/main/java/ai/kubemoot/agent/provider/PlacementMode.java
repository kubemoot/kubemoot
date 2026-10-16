package ai.kubemoot.agent.provider;

import java.util.Locale;
import java.util.Optional;

/**
 * Which placement policy an agent uses, set by the operator from KubemootConfig
 * ({@code spec.defaults.scheduler.placementPolicy}) and the crew's
 * CrewSchedulingPolicy ({@code spec.placementPolicy}).
 */
public enum PlacementMode {
    /** The hand-written placement order in {@link AlgorithmPlacement}. */
    ALGORITHM,
    /** The learned policy decides and places. */
    LEARNED,
    /** The algorithm places; the learned policy's choice is recorded beside it. */
    SHADOW;

    /** The configured value's mode; empty for a blank or unknown value. */
    public static Optional<PlacementMode> parse(String value) {
        if (value == null || value.isBlank()) {
            return Optional.empty();
        }
        try {
            return Optional.of(valueOf(value.trim().toUpperCase(Locale.ROOT)));
        } catch (IllegalArgumentException e) {
            return Optional.empty();
        }
    }

    /** The name used in configuration and CRDs. */
    public String configName() {
        return name().toLowerCase(Locale.ROOT);
    }
}
