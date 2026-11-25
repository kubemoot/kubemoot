package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.modelserver.NatsModelLifecycle;
import ai.kubemoot.agent.modelserver.ModelNotReadyException;

import java.util.Arrays;
import java.util.Collection;
import java.util.EnumMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;

/**
 * The infrastructure reason agents failed for. An agent classifies its own failure text once
 * ({@link #classify}) and publishes the cause's id as the failure signal's
 * {@code metadata.failureType}; the coordinator reads only that id ({@link #fromId}), never
 * the free text. When no agent contributes, the answer names the dominant cause so a person
 * in any chat app learns the crew is unhealthy, not that the question was bad; and a moot
 * where every agent failed for the same cause ends at once instead of waiting out each
 * phase's deadline.
 */
enum FailureCause {
    /** The operator did not answer a request to make a model ready (it is not listening, or did not reply). */
    OPERATOR_UNREACHABLE("operator_unreachable",
            "the crew's models could not be made ready (the operator did not respond)"),
    /** The operator answered, but the server could not make the model ready. */
    MODEL_NOT_READY("model_not_ready", "the crew's models could not be made ready"),
    /** A model server refused the connection. */
    SERVER_UNREACHABLE("server_unreachable", "a model server could not be reached");

    /** What the placement client says when the operator did not respond, from the code that builds the text. */
    private static final List<String> OPERATOR_TEXTS = List.of(
            NatsModelLifecycle.NOT_LISTENING, NatsModelLifecycle.NO_ANSWER,
            NatsModelLifecycle.COULD_NOT_ASK, NatsModelLifecycle.UNREADABLE_REPLY);

    private final String id;
    private final String phrase;

    FailureCause(String id, String phrase) {
        this.id = id;
        this.phrase = phrase;
    }

    /** The stable name published as the failure signal's failureType and in the answer's metadata. */
    String id() {
        return id;
    }

    /** The answer when no agent could contribute because of this cause. */
    String answer() {
        return "No agent could answer: " + phrase + ".";
    }

    /** The cause a published failureType names, or empty for any other failureType. */
    static Optional<FailureCause> fromId(String failureType) {
        return Arrays.stream(values()).filter(c -> c.id.equals(failureType)).findFirst();
    }

    /** Agent side: the cause a failure's text names, or empty when it is not an infrastructure failure. */
    static Optional<FailureCause> classify(String text) {
        if (text == null) {
            return Optional.empty();
        }
        String t = text.toLowerCase(Locale.ROOT);
        if (OPERATOR_TEXTS.stream().anyMatch(known -> t.contains(known.toLowerCase(Locale.ROOT)))) {
            return Optional.of(OPERATOR_UNREACHABLE);
        }
        String notReady = ModelNotReadyException.COULD_NOT_MAKE.toLowerCase(Locale.ROOT);
        if (t.contains(notReady) && t.contains(ModelNotReadyException.READY_SUFFIX.toLowerCase(Locale.ROOT).trim())) {
            return Optional.of(MODEL_NOT_READY);
        }
        if (t.contains("connection refused") || t.contains("connectexception")) {
            return Optional.of(SERVER_UNREACHABLE);
        }
        return Optional.empty();
    }

    /** The cause every one of the failure types names; empty when there are none or any differs or names none. */
    static Optional<FailureCause> unanimous(Collection<String> failureTypes) {
        FailureCause common = null;
        for (String type : failureTypes) {
            var cause = fromId(type);
            if (cause.isEmpty() || (common != null && common != cause.get())) {
                return Optional.empty();
            }
            common = cause.get();
        }
        return Optional.ofNullable(common);
    }

    /** The cause named by most of the failure types (the first declared on a tie); empty when none names one. */
    static Optional<FailureCause> dominant(Collection<String> failureTypes) {
        Map<FailureCause, Integer> counts = new EnumMap<>(FailureCause.class);
        failureTypes.forEach(type -> fromId(type).ifPresent(c -> counts.merge(c, 1, Integer::sum)));
        return counts.entrySet().stream()
                .reduce((a, b) -> b.getValue() > a.getValue() ? b : a)
                .map(Map.Entry::getKey);
    }
}
