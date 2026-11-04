package ai.kubemoot.agent.chat;

import java.util.regex.Pattern;

/**
 * The text form of the file descriptions an agent's contribution carries, shared by the code that
 * writes them ({@link ToolCallLedger}), the compute contract that looks for stored results in its
 * input, and the coordinator that removes the internal lines from the user-facing answer. One
 * definition, so the writer and the readers cannot drift.
 *
 * <p>A result small enough for the discussion ({@link DiscussionRoom}) follows its call line whole,
 * between {@code [RESULT]} and {@code [END RESULT]} ({@code [FAILED RESULT]} when the tool
 * returned an error). Those blocks are data; the other lines only describe files.
 */
public final class CallManifest {

    /** Opens the header line: {@code [WORKSPACE agent=... calls=N errors=E prefix=...]}. */
    public static final String HEADER_PREFIX = "[WORKSPACE ";

    /** Opens a call line: {@code [CALL key=<key> tool=<tool> args=... bytes=... rows=... error=...]}. */
    public static final String CALL_PREFIX = "[CALL key=";

    /** Opens the line of a call whose result could not be written: {@code [NOT STORED tool=<tool> ...]}. */
    public static final String NOT_STORED_PREFIX = "[NOT STORED tool=";

    /** Opens the closing line that says where the results are. */
    public static final String FOOTER_PREFIX = "The results are stored under these keys";

    /** A header line with its line break. */
    public static final Pattern HEADER_LINE = Pattern.compile("\\[WORKSPACE [^\\]]*+\\]\\n?");

    /** A call line: group 1 is the object key, group 2 the tool. */
    public static final Pattern CALL_LINE =
            Pattern.compile("\\[CALL key=([^\\]\\s]++) tool=([^\\]\\s]++)[^\\]]*+\\]");

    /** A not-stored line. */
    public static final Pattern NOT_STORED_LINE = Pattern.compile("\\[NOT STORED tool=[^\\]]*+\\]");

    /** The line that counts the calls a long manifest leaves out. */
    public static final Pattern OMITTED_LINE = Pattern.compile("\\n?\\[\\d++ more calls under the prefix\\]");

    /** The closing line, up to its end. */
    public static final Pattern FOOTER_LINE =
            Pattern.compile("\\n?" + Pattern.quote(FOOTER_PREFIX) + "[^\\n]*+");

    /** Opens the block that holds a call's whole result. */
    public static final String RESULT_OPEN = "[RESULT]";

    /** Opens the block that holds the error text of a failed call. */
    public static final String FAILED_RESULT_OPEN = "[FAILED RESULT]";

    /** Closes either block. */
    public static final String RESULT_CLOSE = "[END RESULT]";

    /** A block of a result; group 1 is the result as the tool returned it. */
    public static final Pattern RESULT_BLOCK = Pattern.compile(
            Pattern.quote(RESULT_OPEN) + "\\n(.*?)\\n" + Pattern.quote(RESULT_CLOSE) + "\\n?", Pattern.DOTALL);

    /** A block of the error text of a failed call; group 1 is the text. */
    public static final Pattern FAILED_RESULT_BLOCK = Pattern.compile(
            Pattern.quote(FAILED_RESULT_OPEN) + "\\n(.*?)\\n" + Pattern.quote(RESULT_CLOSE) + "\\n?",
            Pattern.DOTALL);

    /** Most characters of the descriptions an agent's contribution carries. */
    public static final int MAX_CHARS = 4_000;

    /**
     * Most characters of results one contribution carries. With the descriptions
     * ({@link #MAX_CHARS}) the total stays within the most an agent's statement may hold on the bus.
     */
    public static final int MAX_INLINE_CHARS = 20_000;

    /** Most characters of a call's arguments in a manifest or catalog line. */
    public static final int ARGS_SUMMARY_CHARS = 60;

    private CallManifest() {}

    /** The line, with its leading line break, that says {@code count} calls are not listed. */
    public static String omittedLine(int count) {
        return "\n[" + count + " more calls under the prefix]";
    }

    /**
     * The arguments on one line, cut at {@link #ARGS_SUMMARY_CHARS}. Square brackets become
     * parentheses so a list in the arguments cannot end the [CALL ...] line early.
     */
    public static String argsSummary(String arguments) {
        String one = arguments == null ? ""
                : arguments.replaceAll("\\s+", " ").strip().replace('[', '(').replace(']', ')');
        if (one.isEmpty()) {
            return "{}";
        }
        return one.length() <= ARGS_SUMMARY_CHARS ? one : one.substring(0, ARGS_SUMMARY_CHARS) + "...";
    }
}
