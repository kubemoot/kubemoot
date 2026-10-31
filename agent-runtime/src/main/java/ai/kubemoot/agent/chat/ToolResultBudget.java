package ai.kubemoot.agent.chat;

/**
 * The room one turn's tool results may take in the context window. The room is the
 * window, minus the prompt the model already has, minus a reserve for its reply.
 * Results are admitted in the order they arrive and each one that fits uses up
 * room. A result that does not fit is never shown to the model in part, because half
 * a JSON list misleads it. What replaces it depends on who needs the data: an agent
 * that reads results itself gets a tool error telling it to ask for less; a tooler
 * whose raw output is its contribution gets a {@link ToolResultStandIn}, since the
 * crew receives the full result and the tooler's model only decides what to call
 * next; when less than twice the reply reserve is left in the window, that stand-in drops the
 * head lines and keeps only the one-line summary, so more calls fit before the tooler has to
 * stop. A window of zero or less means the window is unknown, and every result is
 * admitted.
 */
final class ToolResultBudget {

    /** Smallest reserve for the model's reply, in tokens. */
    static final long MIN_REPLY_RESERVE = 1_024;

    /** Share of the window kept for the model's reply when that is larger than the minimum. */
    static final long REPLY_RESERVE_DIVISOR = 8;

    /**
     * What happened to one result: the text for the prompt, whether the prompt shows
     * something else than the result, and the size that decided it.
     */
    record Admission(String text, boolean replaced, long resultTokens, long roomTokens) {}

    private final boolean known;
    private final CharsPerToken ratio;
    private final long replyReserve;
    private long room;

    ToolResultBudget(long contextLength, long promptTokens, CharsPerToken ratio) {
        this.known = contextLength > 0;
        this.ratio = ratio;
        this.replyReserve = replyReserve(contextLength);
        this.room = contextLength - promptTokens - replyReserve;
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

    /**
     * Admit {@code result} of {@code toolName} if it fits the room left. Otherwise replace
     * it with a {@link ToolResultStandIn} when {@code keptForTheCrew} (a tooler's raw
     * output), or with an error asking for a narrower call.
     */
    Admission admit(String toolName, String result, boolean keptForTheCrew) {
        if (!known) {
            return new Admission(result, false, 0, 0);
        }
        long tokens = tokens(length(toolName) + length(result));
        long available = Math.max(room, 0);
        if (tokens <= available) {
            room -= tokens;
            return new Admission(result, false, tokens, available);
        }
        String replacement = keptForTheCrew
                ? ToolResultStandIn.of(toolName, result, room >= replyReserve)
                : errorFor(toolName, tokens, available);
        room -= tokens(length(toolName) + replacement.length());
        return new Admission(replacement, true, tokens, available);
    }

    private String errorFor(String toolName, long tokens, long available) {
        String error = fullError(toolName, tokens, available);
        return tokens(length(toolName) + error.length()) > available ? shortError(toolName) : error;
    }

    private long tokens(long chars) {
        return ratio.tokens(chars);
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
