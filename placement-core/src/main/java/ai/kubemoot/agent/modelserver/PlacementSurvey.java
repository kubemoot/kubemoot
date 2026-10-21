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
    record Survey(String model, List<Surveyed> options, List<Surveyed> elsewhere) {

        /** A survey of one model. */
        Survey(String model, List<Surveyed> options) {
            this(model, options, List.of());
        }

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
            return everything().filter(s -> s.model().equals(chosenModel) && s.server().equals(server)).findFirst();
        }

        private java.util.stream.Stream<Surveyed> everything() {
            return java.util.stream.Stream.concat(options.stream(), elsewhere.stream());
        }

        /**
         * Every option other than {@code chosenModel} on {@code server}, as alternatives:
         * the other servers of the model, then the other acceptable models, which the
         * learned policy weighs for the explanation only (it keeps the crew's model).
         */
        List<PlacementDecision.Alternative> alternativesTo(String chosenModel, String server) {
            List<PlacementDecision.Alternative> out = new ArrayList<>();
            for (Surveyed s : everything().toList()) {
                boolean chosen = s.model().equals(chosenModel) && s.server().equals(server);
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
        String first = null;
        List<Surveyed> options = List.of();
        List<Surveyed> elsewhere = new ArrayList<>();
        for (String model : request.acceptable()) {
            List<Surveyed> surveyed = surveyedOptions(selector, request, model);
            if (surveyed.isEmpty()) {
                continue;
            }
            if (first == null) {
                first = model;
                options = surveyed;
            } else {
                elsewhere.addAll(surveyed);
            }
        }
        return first == null ? Optional.empty() : Optional.of(new Survey(first, options, elsewhere));
    }

    /** The options of {@code model} that run now or soon, with their predictions; empty when it has none. */
    private static List<Surveyed> surveyedOptions(ServerSelector selector, PlacementRequest request, String model) {
        long fp = request.footprint().applyAsLong(model);
        List<ServerSelector.Option> options = fp <= 0 ? List.of()
                : selector.options(model, fp, request.kv().applyAsLong(model), request.promptTokens());
        return options.isEmpty() ? List.of()
                : surveyed(model, options, request.promptTokens(), request.outputTokens().applyAsLong(model));
    }

    /** The options on servers that have the model (loaded or downloaded); others cannot serve the call. */
    private static List<Surveyed> surveyed(String model, List<ServerSelector.Option> options, long promptTokens,
                                           long outputTokens) {
        List<Surveyed> out = new ArrayList<>();
        for (ServerSelector.Option o : options) {
            if (o.server().hasModel(model)) {
                out.add(new Surveyed(model, o, LearnedPlacement.predict(o, model, promptTokens, outputTokens)));
            }
        }
        return out;
    }
}
