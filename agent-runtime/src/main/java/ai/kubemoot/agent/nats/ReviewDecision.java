package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.util.ThinkBlocks;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.Collection;
import java.util.Locale;
import java.util.Optional;

/**
 * The mechanics of the coordinator's review decision: the one decision point
 * after EVALUATING that sets how the crew checks the gathered results before
 * synthesis. The crew's PromptModule holds the policy (when results suffice for
 * a concurrence check, when to escalate); this class holds only what the runtime
 * enforces around it: the data handed to the decision call, the parsing of its
 * answer, and the guards that always escalate.
 *
 * <ul>
 *   <li>{@link Shape#CONCUR}: one analyst is asked whether it concurs; a concern
 *       escalates to a full review.</li>
 *   <li>{@link Shape#FULL}: the review with the selected analysts.</li>
 *   <li>{@link Shape#NONE}: straight to synthesis, where the crew's policy allows it.</li>
 * </ul>
 */
final class ReviewDecision {

    enum Shape {
        CONCUR, FULL, NONE;

        String wireName() {
            return name().toLowerCase(Locale.ROOT);
        }
    }

    /** A decided shape, why, and whether a runtime guard (not the crew's policy) set it. */
    record Decision(Shape shape, String reason, boolean forced) {}

    /** The signal counts the guards read. Counted by the runtime, never by the model. */
    record Evidence(long toolerAgrees, int failures, int concerns, int blocks) {}

    /** The request an analyst receives when asked to concur. */
    static final String CONCURRENCE_REQUEST = "Concurrence check: the crew has the results below for "
            + "this question. Do you concur, or is something missing or wrong? If something is missing "
            + "or wrong, start your reply with CONCERN: and say what.";

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String FIELD_REVIEW = "review";
    private static final String FIELD_REASON = "reason";

    private ReviewDecision() {}

    /**
     * The shape a guard forces, or empty when the crew's policy decides. A failed
     * tooler, a concern, or a block always escalates to a full review, and so do
     * results with no tooler agreement: there is nothing to concur with.
     */
    static Optional<Decision> forced(Evidence e) {
        if (e.failures() > 0) {
            return Optional.of(new Decision(Shape.FULL, "a tooler failed", true));
        }
        if (e.concerns() > 0 || e.blocks() > 0) {
            return Optional.of(new Decision(Shape.FULL, "a concern or objection is on the board", true));
        }
        if (e.toolerAgrees() == 0) {
            return Optional.of(new Decision(Shape.FULL, "no tooler contributed results", true));
        }
        return Optional.empty();
    }

    /**
     * The decision call's answer: {@code {"review": "concur|full|none", "reason": "..."}}.
     * A reasoning block, a code fence, or prose around the object is tolerated.
     * Anything unreadable is a full review. Parsed with readTree only (GraalVM native).
     */
    static Decision parse(String response) {
        String json = jsonObjectIn(response);
        if (json == null) {
            return new Decision(Shape.FULL, "the review decision was not readable", true);
        }
        try {
            var node = MAPPER.readTree(json);
            String review = node.path(FIELD_REVIEW).asText("").trim().toLowerCase(Locale.ROOT);
            String reason = node.path(FIELD_REASON).asText("").trim();
            for (Shape shape : Shape.values()) {
                if (shape.wireName().equals(review)) {
                    return new Decision(shape, reason, false);
                }
            }
            return new Decision(Shape.FULL, "unknown review decision '" + review + "'", true);
        } catch (Exception e) {
            return new Decision(Shape.FULL, "the review decision was not readable", true);
        }
    }

    /** The first-to-last brace span of {@code response} after any reasoning block, or null. */
    static String jsonObjectIn(String response) {
        if (response == null) {
            return null;
        }
        String text = ThinkBlocks.remove(response);
        int start = text.indexOf('{');
        int end = text.lastIndexOf('}');
        return start >= 0 && end > start ? text.substring(start, end + 1) : null;
    }

    /**
     * The user message of the decision call: DATA ONLY. When to concur, escalate, or
     * skip the review is the crew's policy, delivered in the coordinator's system
     * prompt; this message carries the question, the runtime's signal counts, the
     * selected analysts, the gathered results, and a reminder of the JSON shape.
     */
    static String prompt(String question, String signalContext, Collection<String> selectedAnalysts,
                         String results) {
        return "Review decision for this discussion.\n\n"
                + "User question: \"" + question + "\"\n\n"
                + signalContext + "\n"
                + "Analysts selected for this question: "
                + (selectedAnalysts.isEmpty() ? "none" : String.join(", ", selectedAnalysts)) + "\n\n"
                + "Gathered results:\n" + results + "\n"
                + "Follow your instructions for the review decision and respond with one JSON object, "
                + "no markdown fences:\n"
                + "{\"review\": \"concur\" | \"full\" | \"none\", \"reason\": \"one short sentence\"}";
    }
}
