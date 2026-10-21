package ai.kubemoot.agent.modelserver;

import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.Locale;
import java.util.function.Consumer;

/** Builds the placement policy a configuration value selects. */
public final class PlacementPolicies {

    private static final Logger log = LoggerFactory.getLogger(PlacementPolicies.class);

    /** Counts shadow decisions by how the learned choice compared with the algorithm's. */
    static final String METRIC_SHADOW_DECISIONS = "kubemoot_scheduler_shadow_decisions_total";

    private PlacementPolicies() {}

    /**
     * The policy for {@code configured}; an unknown value selects the algorithm, so a
     * typo never leaves an agent without placement.
     */
    public static PlacementPolicy create(String configured, PlacementPolicy algorithm, ServerSelector selector,
                                  Consumer<ShadowPlacement.Decision> recorder) {
        PlacementMode mode = PlacementMode.parse(configured).orElseGet(() -> {
            log.warn("Unknown placement policy '{}', using algorithm", configured);
            return PlacementMode.ALGORITHM;
        });
        if (mode == PlacementMode.ALGORITHM) {
            return algorithm;
        }
        LearnedPlacement learned = new LearnedPlacement(algorithm, selector);
        return mode == PlacementMode.LEARNED ? learned : new ShadowPlacement(algorithm, learned, recorder);
    }

    /** Logs each shadow decision and counts it by agreement; a call retried while waiting counts per attempt. */
    public static Consumer<ShadowPlacement.Decision> shadowRecorder(MeterRegistry registry) {
        return d -> {
            ShadowPlacement.Agreement agreement = d.agreement();
            // A difference is what a shadow run is for, so it logs at INFO; agreement at DEBUG.
            org.slf4j.event.Level level = agreement == ShadowPlacement.Agreement.DIFFER
                    ? org.slf4j.event.Level.INFO : org.slf4j.event.Level.DEBUG;
            log.atLevel(level).log("Shadow placement for {}: algorithm={} learned={} ({})", d.preferred(),
                    d.actual() == null ? "wait" : d.actual().model() + "@" + d.actual().pick().server().name(),
                    d.learned() == null ? "no opinion"
                            : d.learned().model() + "@" + d.learned().server() + " " + d.learned().reason(),
                    agreement.name().toLowerCase(Locale.ROOT));
            if (registry != null) {
                Counter.builder(METRIC_SHADOW_DECISIONS)
                        .description("Placement decisions in shadow mode, by how the learned policy's choice "
                                + "compared with the algorithm's: agree, differ, or no_opinion")
                        .tag("agreement", agreement.name().toLowerCase(Locale.ROOT))
                        .register(registry).increment();
            }
        };
    }
}
