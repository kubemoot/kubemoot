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

    /**
     * Characters per token assumed for a result when the measured ratio is higher. JSON
     * runs near 3 characters per token, so a result is never counted smaller than this.
     */
    static final double CONSERVATIVE_CHARS_PER_TOKEN = 3.0;

    /** What happened to one result: the text for the prompt, and the size that decided it. */
    record Admission(String text, boolean replaced, long resultTokens, long roomTokens) {}

    private final boolean known;
    private final double charsPerToken;
    private long room;

    ToolResultBudget(long contextLength, long promptTokens, CharsPerToken ratio) {
        this.known = contextLength > 0;
        this.charsPerToken = Math.min(ratio.ratio(), CONSERVATIVE_CHARS_PER_TOKEN);
        this.room = contextLength - promptTokens - replyReserve(contextLength);
    }

    /** Tokens kept free for the model's reply in a window of {@code contextLength} tokens. */
    static long replyReserve(long contextLength) {
        return Math.max(MIN_REPLY_RESERVE, contextLength / REPLY_RESERVE_DIVISOR);
    }

    /** A tool error result: {@code {"error": "<message>"}} with the message JSON-escaped. */
    static String errorJson(String message) {
        StringBuilder out = new StringBuilder("{\"error\": \"");
        for (int i = 0; i < message.length(); i++) {
            char c = message.charAt(i);
            switch (c) {
                case '"' -> out.append("\\\"");
                case '\\' -> out.append("\\\\");
                case '\n' -> out.append("\\n");
                case '\r' -> out.append("\\r");
                case '\t' -> out.append("\\t");
                default -> {
                    if (c < 0x20) {
                        out.append(String.format("\\u%04x", (int) c));
                    } else {
                        out.append(c);
                    }
                }
            }
        }
        return out.append("\"}").toString();
    }

    /** Admit {@code result} of {@code toolName} if it fits the room left, else replace it with an error. */
    Admission admit(String toolName, String result) {
        if (!known) {
            return new Admission(result, false, 0, 0);
        }
        long tokens = tokens(length(toolName) + length(result));
        long available = Math.max(room, 0);
        if (tokens <= available) {
            room -= tokens;
            return new Admission(result, false, tokens, available);
        }
        String error = fullError(toolName, tokens, available);
        if (tokens(length(toolName) + error.length()) > available) {
            error = shortError(toolName);
        }
        room -= tokens(length(toolName) + error.length());
        return new Admission(error, true, tokens, available);
    }

    private long tokens(long chars) {
        return chars <= 0 ? 0 : (long) Math.ceil(chars / charsPerToken);
    }

    private static int length(String s) {
        return s == null ? 0 : s.length();
    }

    static String fullError(String toolName, long tokens, long available) {
        return errorJson("Tool result larger than the room left in the context window: " + toolName
                + " returned about " + tokens + " tokens and about " + available + " tokens are left."
                + " The result was not kept. Call " + toolName + " again with a filter: a namespace,"
                + " a label selector, a field selection, a limit, or a shorter time range.");
    }

    /** Used when the room is gone, so many parallel oversized calls cannot fill the window with errors. */
    static String shortError(String toolName) {
        return errorJson(toolName + ": result too large for the context window; call again with a filter or a limit.");
    }
}
