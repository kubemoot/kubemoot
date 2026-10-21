package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Survey;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Surveyed;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Optional;

/**
 * Builds the {@link PlacementDecision} of a placement: the learned policy's from the
 * option it chose, the algorithm's from the fit explanation of the server it picked
 * with the prediction the survey holds for that server.
 */
final class PlacementDecisions {

    private static final Logger log = LoggerFactory.getLogger(PlacementDecisions.class);

    private PlacementDecisions() {}

    /**
     * {@code placed} with its decision: itself when a policy already explained it,
     * else explained as the algorithm's. Explaining never fails a placement that
     * already claimed its ticket: when it throws, the decision is the bare algorithm
     * label with no prediction.
     */
    static Placement explained(Placement placed, Optional<Survey> survey, PlacementMode mode) {
        if (placed.decision() != null) {
            return placed;
        }
        try {
            return placed.withDecision(byAlgorithm(placed, survey, mode));
        } catch (RuntimeException e) {
            log.debug("Explaining a placement failed: {}", e.getMessage());
            return placed.withDecision(new PlacementDecision(PlacementDecision.ALGORITHM,
                    "placed by the algorithm", List.of(), null));
        }
    }

    /** The decision of a call the learned policy placed on {@code chosen}. */
    static PlacementDecision learned(Surveyed chosen, Survey survey) {
        return new PlacementDecision(PlacementDecision.LEARNED, chosen.prediction().orElseThrow().explain(),
                survey.alternativesTo(chosen.model(), chosen.server()), chosen.prediction().orElse(null));
    }

    /**
     * The decision of a call the algorithm placed. {@code mode} is the configured
     * policy: under {@link PlacementMode#SHADOW} the call is labeled shadow and the
     * reason says what the learned policy would have chosen; under
     * {@link PlacementMode#LEARNED} the learned policy deferred to the algorithm and
     * the reason says why. The prediction is the survey's for the server the
     * algorithm picked, when it weighed that server.
     */
    static PlacementDecision byAlgorithm(Placement placed, Optional<Survey> survey, PlacementMode mode) {
        String server = placed.pick().server().name();
        Optional<Surveyed> chosen = survey.flatMap(s -> s.of(placed.model(), server));
        String reason = algorithmReason(placed) + learnedNote(placed, survey, mode);
        var alternatives = survey.map(s -> s.alternativesTo(placed.model(), server)).orElse(List.of());
        String policy = mode == PlacementMode.SHADOW ? PlacementDecision.SHADOW : PlacementDecision.ALGORITHM;
        return new PlacementDecision(policy, reason, alternatives,
                chosen.flatMap(Surveyed::prediction).orElse(null));
    }

    /** The fit explanation of the picked server, with any models unloaded and any queueing. */
    static String algorithmReason(Placement placed) {
        var score = placed.pick().score();
        String fit = score == null || score.reasoning() == null ? "" : score.reasoning();
        StringBuilder reason = new StringBuilder(fit.isBlank() ? "placed by the algorithm" : fit);
        if (!placed.evicted().isEmpty()) {
            reason.append("; unloaded ").append(String.join(", ", placed.evicted())).append(" to make room");
        }
        if (placed.queued()) {
            reason.append("; joined a busy copy and may wait for a slot");
        }
        return reason.toString();
    }

    /** What the learned policy thought, for the modes where it is consulted. */
    private static String learnedNote(Placement placed, Optional<Survey> survey, PlacementMode mode) {
        if (mode == PlacementMode.ALGORITHM) {
            return "";
        }
        Optional<Surveyed> soonest = survey.flatMap(Survey::soonest);
        if (soonest.isEmpty()) {
            return "; learned policy has no opinion (a server's speeds are not measured)";
        }
        Surveyed s = soonest.get();
        String choice = s.model() + "@" + s.server() + " " + s.prediction().orElseThrow().explain();
        boolean same = s.model().equals(placed.model()) && s.server().equals(placed.pick().server().name());
        if (same) {
            return "; learned policy agrees: " + choice;
        }
        if (mode == PlacementMode.SHADOW) {
            return "; learned policy would choose " + choice;
        }
        return s.option().startsNow()
                ? "; the learned choice " + choice + " was taken first"
                : "; the learned choice " + choice + " has to wait for a slot";
    }
}
