package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;

import java.util.List;

/**
 * Why a call was placed where it was: which policy placed it, the reason that
 * policy gave, the other servers that could have run the model, and the completion
 * time predicted for the chosen one. It rides on the {@link CallPlanner.Placement}
 * and is what the discussion receipt later sets beside what the call measured.
 *
 * @param policy       {@link #LEARNED}, {@link #ALGORITHM} or {@link #SHADOW}
 * @param reason       the policy's own explanation of the choice
 * @param alternatives the other servers that could have run the model, with their predictions
 * @param predicted    the predicted completion time of the chosen server; null when its speeds are not measured
 */
public record PlacementDecision(String policy, String reason, List<Alternative> alternatives,
                                Prediction predicted) {

    /** The learned policy chose the server from predicted completion time. */
    public static final String LEARNED = "learned";
    /** The hand-written algorithm placed the call. */
    public static final String ALGORITHM = "algorithm";
    /** The algorithm placed the call while the learned policy only recorded its choice. */
    public static final String SHADOW = "shadow";

    /**
     * Another server the model could have run on.
     *
     * @param server    the ModelServer
     * @param model     the model considered there
     * @param warm      a copy of the model was resident or loading there
     * @param startsNow the server had a free slot
     * @param predicted the predicted completion time there; null when the server's speeds are not measured
     */
    public record Alternative(String server, String model, boolean warm, boolean startsNow, Prediction predicted) {}

    /**
     * This decision for a call that was planned at selection: the load and the wait
     * for a slot started then and overlapped the agent's triage, so the first request
     * is predicted without them.
     */
    public PlacementDecision plannedAtSelection() {
        Prediction rebased = predicted == null ? null : new Prediction(0.0, 0.0, predicted.prompt(), predicted.generate());
        return new PlacementDecision(policy, reason + "; planned at selection, so the wait and load overlapped "
                + "the agent's triage and are not counted", alternatives, rebased);
    }

    public PlacementDecision {
        policy = policy == null ? ALGORITHM : policy;
        reason = reason == null ? "" : reason;
        alternatives = alternatives == null ? List.of() : List.copyOf(alternatives);
    }
}
