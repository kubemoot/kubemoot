package ai.kubemoot.agent.chat;

/** Cuts text to a length, the one way a tool result is cut for a contribution or a stored object. */
final class TextCut {

    /** What a cut result ends with. */
    static final String MARKER = " ...[truncated]";

    private TextCut() {}

    /** {@code s} cut to {@code max} characters with {@link #MARKER} appended when it was longer; "" for null. */
    static String truncate(String s, int max) {
        if (s == null) {
            return "";
        }
        return s.length() <= max ? s : s.substring(0, max) + MARKER;
    }
}
