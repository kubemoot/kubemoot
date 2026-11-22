package ai.kubemoot.agent.nats;

import java.util.Locale;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * A ratifier's verdict on a draft: what it published, and the objection it carries. An agent's
 * reply opens with AGREE, CONCERN or BLOCK; the rest of the reply is the reason, and for a
 * concern or a block it is the objection. A reply that does not open with one of them is not a
 * verdict, and the agent reports it as a failure rather than letting silence count as consent.
 */
record RatifyReply(Kind kind, String text) {

    /** What a ratifier answered. */
    enum Kind {
        AGREE("agree"), CONCERN("concern"), BLOCK("block"), STAND_ASIDE("stand_aside"), FAILURE("failure");

        private final String signal;

        Kind(String signal) {
            this.signal = signal;
        }

        /** The message type the verdict is published as. */
        String signal() {
            return signal;
        }

        /** True for a verdict that objects to the draft. */
        boolean objects() {
            return this == CONCERN || this == BLOCK;
        }

        private static final java.util.Map<String, Kind> BY_SIGNAL = java.util.Map.of(
                "agree", AGREE, "concern", CONCERN, "block", BLOCK, "stand_aside", STAND_ASIDE,
                "decline", STAND_ASIDE, "failure", FAILURE);

        /** The kind published as {@code signal}, or null when it is not a ratification signal. */
        static Kind ofSignal(String signal) {
            return signal == null ? null : BY_SIGNAL.get(signal);
        }
    }

    private static final Pattern VERDICT = Pattern.compile(
            "^\\W*(AGREED?|CONCERN|BLOCK)\\b[\\s:.\\-]*(.*)$", Pattern.CASE_INSENSITIVE | Pattern.DOTALL);

    /** The verdict in {@code modelText}, or null when the reply opens with none. */
    static RatifyReply parse(String modelText) {
        if (modelText == null) {
            return null;
        }
        Matcher m = VERDICT.matcher(modelText.strip());
        if (!m.matches()) {
            return null;
        }
        String word = m.group(1).toUpperCase(Locale.ROOT);
        Kind kind = word.startsWith("AGREE") ? Kind.AGREE : Kind.valueOf(word);
        return new RatifyReply(kind, m.group(2).strip());
    }
}
