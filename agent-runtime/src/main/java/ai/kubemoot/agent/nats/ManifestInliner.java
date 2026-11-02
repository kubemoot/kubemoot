package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.CallManifest;
import java.util.OptionalLong;
import java.util.function.Function;
import java.util.function.ToLongFunction;
import java.util.regex.Matcher;

/**
 * Fills a tooler's manifest in with the stored results it names, for the synthesizer. The
 * synthesizer has no tools and cannot read the workspace, so a manifest alone would hand it keys
 * and no data. Each {@code [CALL key=... tool=...]} line becomes {@code [tool]} and the stored result,
 * the form the glued contribution has. A result that cannot be read becomes the same explicit
 * "unavailable" notice a spilled artifact gets, so the synthesizer reports the gap instead of
 * answering over data it never saw. The manifest's own header and closing lines are removed.
 *
 * <p>How much is filled in depends on the room the synthesizer's window has left. Results are
 * filled in manifest order, each whole or not at all, until one does not fit; that one and the
 * rest keep their call line and get a note that they are not included, so the synthesizer knows
 * the data exists and that the analysts' findings summarize it. When the window is unknown, each
 * result is filled in up to a fixed number of characters instead.
 */
final class ManifestInliner {

    /** The note that follows the call line of a result that did not fit. */
    static final String NOT_INCLUDED = "Not included here: the stored result did not fit the room left in the "
            + "synthesis window. The analysts' findings summarize it.";

    /** What a result cut at the fixed character limit ends with (the limit applies when the window is unknown). */
    static final String CUT_NOTE = "\n[...artifact truncated for synthesis...]";

    private ManifestInliner() {}

    /**
     * {@code content} as it is, or cut to {@code max} characters and ended with {@link #CUT_NOTE}.
     * The one cut a synthesis input makes, for a spilled artifact and for a result when the window
     * is unknown.
     */
    static String cut(String content, int max) {
        return content.length() > max ? content.substring(0, max) + CUT_NOTE : content;
    }

    /** The room and the way to count it, for one pass over a text. */
    private static final class Fill {
        final Function<String, String> read;
        final ToLongFunction<String> tokens;
        final boolean windowKnown;
        final int fallbackChars;
        long room;
        boolean full;

        Fill(Function<String, String> read, ToLongFunction<String> tokens, OptionalLong roomTokens, int fallbackChars) {
            this.read = read;
            this.tokens = tokens;
            this.windowKnown = roomTokens.isPresent();
            this.room = roomTokens.orElse(0);
            this.fallbackChars = fallbackChars;
        }

        /** The text that replaces one call line. */
        String replacement(String callLine, String key, String tool) {
            if (full || windowKnown && room <= 0) {
                full = true;
                return notIncluded(callLine);
            }
            String content = read.apply(key);
            if (content == null) {
                return DiscussionOrchestrator.artifactUnavailableNotice(key);
            }
            return windowKnown ? withinRoom(callLine, tool, content) : withinFallback(tool, content);
        }

        private String withinRoom(String callLine, String tool, String content) {
            String filled = "[" + tool + "]\n" + content;
            long cost = tokens.applyAsLong(filled);
            if (cost > room) {
                full = true;
                return notIncluded(callLine);
            }
            room -= cost;
            return filled;
        }

        private String withinFallback(String tool, String content) {
            return "[" + tool + "]\n" + cut(content, fallbackChars);
        }

        private static String notIncluded(String callLine) {
            return callLine + "\n" + NOT_INCLUDED;
        }
    }

    /**
     * {@code text} with every manifest call line replaced by its stored result, or by the call line
     * and a note when it does not fit. {@code read} returns a result's whole text, or null when it
     * cannot be read. {@code tokens} estimates the tokens of a text; {@code roomTokens} is the room
     * the synthesis window has left for results, empty when the window is unknown, in which case each
     * result is cut at {@code fallbackChars} characters instead.
     */
    static String inline(String text, Function<String, String> read, ToLongFunction<String> tokens,
                         OptionalLong roomTokens, int fallbackChars) {
        if (text == null || !text.contains(CallManifest.CALL_PREFIX)) {
            return text;
        }
        var fill = new Fill(read, tokens, roomTokens, fallbackChars);
        Matcher m = CallManifest.CALL_LINE.matcher(strip(text, false));
        var sb = new StringBuilder();
        while (m.find()) {
            m.appendReplacement(sb, Matcher.quoteReplacement(fill.replacement(m.group(), m.group(1), m.group(2))));
        }
        m.appendTail(sb);
        return sb.toString();
    }

    /**
     * {@code text} without the manifest's header and closing lines and, when {@code withCalls}, its
     * call lines too. The user-facing answer is stripped of all three, so no internal key reaches it.
     */
    static String strip(String text, boolean withCalls) {
        String out = CallManifest.FOOTER_LINE.matcher(CallManifest.HEADER_LINE.matcher(text).replaceAll(""))
                .replaceAll("");
        return withCalls ? CallManifest.CALL_LINE.matcher(out).replaceAll("") : out;
    }
}
