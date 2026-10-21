package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;

import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

/**
 * Every server that could run a call, each with the completion time predicted for it,
 * read before anything is claimed. The learned policy ranks it; the other policies'
 * placements are explained and scored against it, so every placed call has a
 * prediction to compare with what the call measures, whichever policy placed it.
 */
final class PlacementSurvey {

    private PlacementSurvey() {}

    /** One server with its prediction; empty when the server's speeds for the model are not measured. */
    record Surveyed(String model, ServerSelector.Option option, Optional<Prediction> prediction) {
        String server() {
            return option.server().name();
        }
    }

    /** The options of the first model, in the request's order, that has any. */
    record Survey(String model, List<Surveyed> options) {

        /**
         * The option predicted to finish soonest; empty when there is none or when any
         * option is unmeasured, so an unmeasured server is never guessed fast.
         */
        Optional<Surveyed> soonest() {
            Surveyed best = null;
            for (Surveyed s : options) {
                if (s.prediction().isEmpty()) {
                    return Optional.empty();
                }
                if (best == null || s.prediction().get().total() < best.prediction().get().total()) {
                    best = s;
                }
            }
            return Optional.ofNullable(best);
        }

        /** The option of {@code model} on {@code server}, when this survey weighed it. */
        Optional<Surveyed> of(String chosenModel, String server) {
            if (!model.equals(chosenModel)) {
                return Optional.empty();
            }
            return options.stream().filter(s -> s.server().equals(server)).findFirst();
        }

        /** The options other than {@code chosenModel} on {@code server}, as alternatives. */
        List<PlacementDecision.Alternative> alternativesTo(String chosenModel, String server) {
            List<PlacementDecision.Alternative> out = new ArrayList<>();
            for (Surveyed s : options) {
                boolean chosen = model.equals(chosenModel) && s.server().equals(server);
                if (!chosen) {
                    out.add(new PlacementDecision.Alternative(s.server(), s.model(), s.option().warm(),
                            s.option().startsNow(), s.prediction().orElse(null)));
                }
            }
            return out;
        }
    }

    /**
     * The survey for the first model of the request that has any option on a server
     * that has it; empty when the request has no usable model or {@code selector} is
     * null.
     */
    static Optional<Survey> of(ServerSelector selector, PlacementRequest request) {
        if (selector == null) {
            return Optional.empty();
        }
        for (String model : request.acceptable()) {
            long fp = request.footprint().applyAsLong(model);
            List<ServerSelector.Option> options = fp <= 0 ? List.of()
                    : selector.options(model, fp, request.kv().applyAsLong(model), request.promptTokens());
            if (!options.isEmpty()) {
                return Optional.of(new Survey(model, surveyed(model, options, request.promptTokens())));
            }
        }
        return Optional.empty();
    }

    /** The options on servers that have the model (loaded or downloaded); others cannot serve the call. */
    private static List<Surveyed> surveyed(String model, List<ServerSelector.Option> options, long promptTokens) {
        List<Surveyed> out = new ArrayList<>();
        for (ServerSelector.Option o : options) {
            if (o.server().hasModel(model)) {
                out.add(new Surveyed(model, o, LearnedPlacement.predict(o, model, promptTokens)));
            }
        }
        return out;
    }
}
