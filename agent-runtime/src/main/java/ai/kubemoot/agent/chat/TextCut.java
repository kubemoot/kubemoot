package ai.kubemoot.agent.chat;

/** Cuts text to a length, the one way a tool result is cut for a contribution or a stored object. */
public final class TextCut {

    /** What a cut result ends with. */
    public static final String MARKER = " ...[truncated]";

    private TextCut() {}

    /** {@code s} cut to {@code max} characters with {@link #MARKER} appended when it was longer; "" for null. */
    public static String truncate(String s, int max) {
        if (s == null) {
            return "";
        }
        return s.length() <= max ? s : prefix(s, max) + MARKER;
    }

    /**
     * The first {@code max} characters of {@code s} or fewer: a cut never leaves half of a surrogate
     * pair (an emoji) at its end. "" for null.
     */
    public static String prefix(String s, int max) {
        if (s == null || max <= 0) {
            return "";
        }
        if (s.length() <= max) {
            return s;
        }
        int end = max;
        if (Character.isHighSurrogate(s.charAt(end - 1)) && Character.isLowSurrogate(s.charAt(end))) {
            end--;
        }
        return s.substring(0, end);
    }
}
