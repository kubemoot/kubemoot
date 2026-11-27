package ai.kubemoot.agent.nats;

/**
 * The words of the clarifying-question round, shared by the coordinator that relays a question
 * and the agents that answer it: the message types, their metadata fields, and the short turn
 * instructions the runtime ends an agent's message with. What an agent should check stays in
 * the crew's prompts; these only say what the turn is.
 */
final class RecheckText {

    /** The coordinator relays a question to the agent that made the finding. */
    static final String MSG_QUESTION_ASKED = "question_asked";
    /** The coordinator wakes the asker with the revised finding. */
    static final String MSG_QUESTION_ANSWERED = "question_answered";

    /** The question a message relays or answers. */
    static final String FIELD_QUESTION_ID = "questionId";
    /** Who asked the question. */
    static final String FIELD_ASKED_BY = "askedBy";
    /** Who the question is to, and who answered it. */
    static final String FIELD_ANSWERED_BY = "answeredBy";
    /** On an agent's signal: the question it answers, so its finding replaces its earlier one. */
    static final String FIELD_ANSWERS = "answers";

    private RecheckText() {
    }

    /** What the coordinator relays to {@code target}: who asks, and the question. */
    static String relayed(String asker, String target, String question) {
        return "@" + target + ", " + asker + " asks: " + question;
    }

    /** What the coordinator tells {@code asker}: who rechecked, and its revised finding. */
    static String answered(String asker, String target, String finding) {
        return "@" + asker + ", " + target + " rechecked in answer to your question. Its revised finding:\n\n" + finding;
    }

    /** The end of the message the asked agent answers. */
    static String recheckInstruction(String asker) {
        return "[RECHECK] " + asker + " asked you the question above about your finding. Run your assessment again "
                + "with your tools, answer the question in your reply, and record your verdict with the signal tool. "
                + "Your reply replaces your earlier finding.";
    }

    /** The end of the message the asker answers once its question is answered. */
    static String answeredInstruction(String target) {
        return "[RECHECK ANSWERED] " + target + " answered your question above with a revised finding. Check it "
                + "again and record your verdict with the signal tool.";
    }
}
