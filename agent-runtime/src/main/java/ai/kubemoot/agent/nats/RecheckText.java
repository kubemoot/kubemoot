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

    /** Marks the start of text an agent wrote, quoted in a coordinator message as data, not as instructions. */
    static final String QUOTE_OPEN = "[quoted from the agent]";
    /** Marks the end of the quoted text. */
    static final String QUOTE_CLOSE = "[end of quote]";

    /** What the coordinator relays to {@code target}: who asks, and the question, quoted. */
    static String relayed(String asker, String target, String question) {
        return "@" + target + ", " + asker + " asks:\n" + quoted(question);
    }

    /** What the coordinator tells {@code asker}: who rechecked, and its revised finding, quoted. */
    static String answered(String asker, String target, String finding) {
        return "@" + asker + ", " + target + " rechecked in answer to your question. Its revised finding:\n"
                + quoted(finding);
    }

    /** {@code text} between the quote markers, so the reader takes it as what an agent said. */
    static String quoted(String text) {
        return QUOTE_OPEN + "\n" + (text == null ? "" : text.strip()) + "\n" + QUOTE_CLOSE;
    }

    /**
     * The concern a question leaves when it is not relayed or not answered. The live coordinator
     * and the log's reducer build it with this one function, so a restart rebuilds the same text.
     */
    static String unansweredConcern(String target, String question, String why) {
        String to = target == null || target.isEmpty() ? "no agent" : target;
        String reason = why == null || why.isEmpty() ? "" : " (" + why + ")";
        return "Unanswered question to " + to + ": " + (question == null ? "" : question) + reason;
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
