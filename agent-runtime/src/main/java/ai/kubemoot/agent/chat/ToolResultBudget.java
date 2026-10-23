package ai.kubemoot.agent.chat;

/**
 * The room one turn's tool results may take in the context window. The room is the
 * window, minus the prompt the model already has, minus a reserve for its reply.
 * Results are admitted in the order they arrive and each one that fits uses up
 * room; a result that does not fit is replaced whole by a tool error that tells the
 * model to ask for less, because half a JSON list misleads the model. A window of
 * zero or less means the window is unknown, and every result is admitted.
 */
final class ToolResultBudget {

    /** Smallest reserve for the model's reply, in tokens. */
    static final long MIN_REPLY_RESERVE = 1_024;

    /** Share of the window kept for the model's reply when that is larger than the minimum. */
    static final long REPLY_RESERVE_DIVISOR = 8;

    /** What happened to one result: the text for the prompt, and the size that decided it. */
    record Admission(String text, boolean replaced, long resultTokens, long roomTokens) {}

    private final boolean known;
    private final CharsPerToken ratio;
    private long room;

    ToolResultBudget(long contextLength, long promptTokens, CharsPerToken ratio) {
        this.known = contextLength > 0;
        this.ratio = ratio;
        this.room = contextLength - promptTokens - replyReserve(contextLength);
    }

    /** Tokens kept free for the model's reply in a window of {@code contextLength} tokens. */
    static long replyReserve(long contextLength) {
        return Math.max(MIN_REPLY_RESERVE, contextLength / REPLY_RESERVE_DIVISOR);
    }

    /** Admit {@code result} of {@code toolName} if it fits the room left, else replace it with an error. */
    Admission admit(String toolName, String result) {
        if (!known) {
            return new Admission(result, false, 0, 0);
        }
        long tokens = ratio.tokens(result == null ? 0 : result.length());
        long available = Math.max(room, 0);
        if (tokens <= available) {
            room -= tokens;
            return new Admission(result, false, tokens, available);
        }
        String error = errorFor(toolName, tokens, available);
        room -= ratio.tokens(error.length());
        return new Admission(error, true, tokens, available);
    }

    private static String errorFor(String toolName, long tokens, long available) {
        return "{\"error\": \"Tool result larger than the room left in the context window: " + toolName
                + " returned about " + tokens + " tokens and about " + available + " tokens are left."
                + " The result was not kept. Call " + toolName + " again with a filter: a namespace,"
                + " a label selector, a field selection, a limit, or a shorter time range.\"}";
    }
}
