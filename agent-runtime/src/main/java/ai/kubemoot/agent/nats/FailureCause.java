package ai.kubemoot.agent.nats;

import java.util.Collection;
import java.util.EnumMap;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;

/**
 * The infrastructure reason agents failed for, read from the text of their failure signals.
 * When no agent contributes, the answer names the dominant cause so a person in any chat app
 * learns the crew is unhealthy, not that the question was bad; and a moot where every agent
 * failed for the same cause ends at once instead of waiting out each phase's deadline.
 */
enum FailureCause {
    /** The operator did not answer a request to make a model ready (it is not listening, or did not reply). */
    OPERATOR_UNREACHABLE("operator_unreachable",
            "the crew's models could not be made ready (the operator did not respond)"),
    /** The operator answered, but the server could not make the model ready. */
    MODEL_NOT_READY("model_not_ready", "the crew's models could not be made ready"),
    /** A model server refused the connection. */
    SERVER_UNREACHABLE("server_unreachable", "a model server could not be reached");

    private final String id;
    private final String phrase;

    FailureCause(String id, String phrase) {
        this.id = id;
        this.phrase = phrase;
    }

    /** The stable name published in the answer's metadata. */
    String id() {
        return id;
    }

    /** The answer when no agent could contribute because of this cause. */
    String answer() {
        return "No agent could answer: " + phrase + ".";
    }

    /** The cause a failure signal's text names, or empty when it is not an infrastructure failure. */
    static Optional<FailureCause> classify(String text) {
        if (text == null) {
            return Optional.empty();
        }
        String t = text.toLowerCase(Locale.ROOT);
        if (t.contains("no operator is listening") || t.contains("the operator did not answer")
                || t.contains("the operator could not be asked") || t.contains("the operator's reply could not be read")) {
            return Optional.of(OPERATOR_UNREACHABLE);
        }
        if (t.contains("could not make") && t.contains(" ready")) {
            return Optional.of(MODEL_NOT_READY);
        }
        if (t.contains("connection refused") || t.contains("connectexception")) {
            return Optional.of(SERVER_UNREACHABLE);
        }
        return Optional.empty();
    }

    /** The cause every one of the failure texts names; empty when there are none or any differs or names none. */
    static Optional<FailureCause> unanimous(Collection<String> failureTexts) {
        FailureCause common = null;
        for (String text : failureTexts) {
            var cause = classify(text);
            if (cause.isEmpty() || (common != null && common != cause.get())) {
                return Optional.empty();
            }
            common = cause.get();
        }
        return Optional.ofNullable(common);
    }

    /** The cause named by most of the failure texts (the first declared on a tie); empty when none names one. */
    static Optional<FailureCause> dominant(Collection<String> failureTexts) {
        Map<FailureCause, Integer> counts = new EnumMap<>(FailureCause.class);
        failureTexts.forEach(text -> classify(text).ifPresent(c -> counts.merge(c, 1, Integer::sum)));
        return counts.entrySet().stream()
                .reduce((a, b) -> b.getValue() > a.getValue() ? b : a)
                .map(Map.Entry::getKey);
    }
}
