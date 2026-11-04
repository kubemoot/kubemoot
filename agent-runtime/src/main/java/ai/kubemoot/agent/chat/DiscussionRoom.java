package ai.kubemoot.agent.chat;

import java.util.OptionalLong;

/**
 * How much text of a thread's tool results the discussion itself may hold, computed from the
 * context window of the models that read the discussion (the smallest window known, so the
 * reader with the least room can still take it in). Nothing here is a size tuned to one
 * cluster: every number follows from the window.
 *
 * <p>The room is a quarter of the window, counted in characters at three characters a token. A
 * result is posted in the discussion only if it fits in whole:
 *
 * <ul>
 *   <li>one result may take an eighth of the room ({@link #perResultChars});
 *   <li>one contribution may carry a quarter of the room in results ({@link #perContributionChars}),
 *       so several small results fit, and never more than {@link CallManifest#MAX_INLINE_CHARS}, so a
 *       contribution always stays under the bus message size.
 * </ul>
 *
 * A result that does not fit stays in its file, described by its call line. When no window is
 * known the room is a fixed {@link #UNKNOWN_WINDOW_CHARS}.
 */
public final class DiscussionRoom {

    /** The room, in characters, when no window is known. */
    public static final int UNKNOWN_WINDOW_CHARS = 8_000;

    private static final int CHARS_PER_TOKEN = 3;
    /** The room is this fraction of the window: {@code 1 / WINDOW_SHARE}. */
    private static final int WINDOW_SHARE = 4;
    private static final int RESULT_SHARE = 8;
    private static final int CONTRIBUTION_SHARE = 4;

    private DiscussionRoom() {}

    /** Characters of a quarter of {@code windowTokens}, or {@link #UNKNOWN_WINDOW_CHARS} when it is empty. */
    public static int chars(OptionalLong windowTokens) {
        return windowTokens.isEmpty() ? UNKNOWN_WINDOW_CHARS
                : (int) Math.min(Integer.MAX_VALUE, windowTokens.getAsLong() / WINDOW_SHARE * CHARS_PER_TOKEN);
    }

    /** Most characters of one result posted in the discussion. */
    public static int perResultChars(OptionalLong windowTokens) {
        return chars(windowTokens) / RESULT_SHARE;
    }

    /** Most characters of results one contribution carries. */
    public static int perContributionChars(OptionalLong windowTokens) {
        return Math.min(chars(windowTokens) / CONTRIBUTION_SHARE, CallManifest.MAX_INLINE_CHARS);
    }
}
