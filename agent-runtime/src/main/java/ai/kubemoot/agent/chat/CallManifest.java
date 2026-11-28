package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.WorkspaceTools;
import java.util.List;
import java.util.regex.Pattern;

/**
 * The text form of the file descriptions an agent's contribution carries, shared by the code that
 * writes them ({@link ToolCallLedger}), the compute contract that looks for stored results in its
 * input, and the coordinator that removes the internal lines from the user-facing answer. One
 * definition, so the writer and the readers cannot drift.
 *
 * <p>A result small enough for the discussion ({@link DiscussionRoom}) follows its call line whole,
 * between {@code [RESULT]} and {@code [END RESULT]} ({@code [FAILED RESULT]} when the tool
 * returned an error). A call that succeeded with an empty result (nothing, or only white space)
 * always has the block {@code [EMPTY RESULT]} instead, so an empty result is never mistaken for a
 * result that is only in its file. The result blocks are data; the other lines only describe files.
 */
public final class CallManifest {

    /** Opens the header line: {@code [WORKSPACE agent=... calls=N errors=E prefix=...]}. */
    public static final String HEADER_PREFIX = "[WORKSPACE ";

    /** Opens a call line: {@code [CALL key=<key> tool=<tool> call=<key>.call args=... bytes=... rows=... error=...]}. */
    public static final String CALL_PREFIX = "[CALL key=";

    /** Opens the line of a call whose result could not be written: {@code [NOT STORED tool=<tool> ...]}. */
    public static final String NOT_STORED_PREFIX = "[NOT STORED tool=";

    /**
     * Opens the line of a call by an agent that does not store its results:
     * {@code [CALL tool=<tool> args=... type=... bytes=... rows=... error=... stored=no]}. It has no
     * key, because nothing was stored to read; a posted result follows it. A result that is not
     * posted gets a {@link #NOT_STORED_PREFIX} line with {@link #NOT_KEPT_NOTE}.
     */
    public static final String UNSTORED_CALL_PREFIX = "[CALL tool=";

    /** What a {@code [NOT STORED ...]} line says of a call whose agent stores nothing and whose result is not posted. */
    public static final String NOT_KEPT_NOTE = "the agent does not store results and this one is not shown here, so it is not available";

    /** What a {@code [NOT STORED ...]} line says of a call whose write failed. */
    public static final String WRITE_FAILED_NOTE = "the result could not be written to the workspace and is not available";

    /**
     * What is appended to a result's key to name the call's working: its arguments exactly as the
     * model sent them (the program, the query, the parameters), stored beside the result.
     */
    public static final String WORKING_SUFFIX = ".call";

    /** The field of a stored call line that names its working's key: {@code call=<agent>/<nnn>-<tool>.call}. */
    public static final String WORKING_FIELD = "call=";

    /** The value of {@link #WORKING_FIELD} when the result is stored and its working could not be written. */
    public static final String WORKING_UNAVAILABLE = "unavailable";

    /** What the closing line says of the workings, so a reader knows the call= key opens the call itself. */
    public static final String WORKING_NOTE = "each call's full arguments (the program of " + WorkspaceTools.CODE_TOOL + ", the query, "
            + "the parameters) are stored under its key followed by " + WORKING_SUFFIX
            + " (the call= key), read like any result";

    /** The key of the working of the call whose result is stored under {@code resultKey}. */
    public static String workingKeyOf(String resultKey) {
        return resultKey + WORKING_SUFFIX;
    }

    /** Opens the closing line that says where the results are. */
    public static final String FOOTER_PREFIX = "The results are stored under these keys";

    /** A header line with its line break. */
    public static final Pattern HEADER_LINE = Pattern.compile("\\[WORKSPACE [^\\]]*+\\]\\n?");

    /** A call line: group 1 is the object key, group 2 the tool. */
    public static final Pattern CALL_LINE =
            Pattern.compile("\\[CALL key=([^\\]\\s]++) tool=([^\\]\\s]++)[^\\]]*+\\]");

    /** A call line of an agent that stores nothing. */
    public static final Pattern UNSTORED_CALL_LINE = Pattern.compile("\\[CALL tool=[^\\]]*+\\]");

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

    /** Opens the block that says a successful call returned nothing. */
    public static final String EMPTY_RESULT_OPEN = "[EMPTY RESULT]";

    /** The whole block of a successful call whose result is empty. */
    public static final String EMPTY_RESULT_TEXT = EMPTY_RESULT_OPEN
            + "\n(empty: the call succeeded and returned nothing)\n[END RESULT]";

    /** The block of a failed call that returned no text. */
    public static final String FAILED_NO_TEXT_TEXT = FAILED_RESULT_OPEN
            + "\n(the call failed and returned no text)\n[END RESULT]";

    /** The blocks the ledger writes itself: they hold no data, and a call that has one is dropped before a call with data. */
    public static final List<String> NO_DATA_BLOCKS = List.of(EMPTY_RESULT_TEXT, FAILED_NO_TEXT_TEXT);

    /** Closes each block. */
    public static final String RESULT_CLOSE = "[END RESULT]";

    /** A block of a result; group 1 is the result as the tool returned it. */
    public static final Pattern RESULT_BLOCK = Pattern.compile(
            Pattern.quote(RESULT_OPEN) + "\\n(.*?)\\n" + Pattern.quote(RESULT_CLOSE) + "\\n?", Pattern.DOTALL);

    /** A block of the error text of a failed call; group 1 is the text. */
    public static final Pattern FAILED_RESULT_BLOCK = Pattern.compile(
            Pattern.quote(FAILED_RESULT_OPEN) + "\\n(.*?)\\n" + Pattern.quote(RESULT_CLOSE) + "\\n?",
            Pattern.DOTALL);

    /** The text that marks the start of any call line, stored or not. */
    public static final List<String> CALL_PREFIXES = List.of(CALL_PREFIX, UNSTORED_CALL_PREFIX);

    /** The markers that open a result block: a result, an error text, an empty result. */
    public static final List<String> RESULT_OPENERS = List.of(RESULT_OPEN, FAILED_RESULT_OPEN, EMPTY_RESULT_OPEN);

    /** The line that counts the calls an agent that stores nothing leaves out: their results are not available. */
    public static final Pattern OMITTED_UNSTORED_LINE = Pattern.compile(
            "\\n?\\[\\d++ more calls not listed; this agent stores no results, so theirs are not available\\]");

    /** The block of an empty result, with its line break. */
    public static final Pattern EMPTY_RESULT_BLOCK = Pattern.compile(Pattern.quote(EMPTY_RESULT_TEXT) + "\\n?");

    /** Most characters of the descriptions an agent's contribution carries. */
    public static final int MAX_CHARS = 4_000;

    /**
     * Most characters of results one contribution carries. With the descriptions
     * ({@link #MAX_CHARS}) the total stays within the most an agent's statement may hold on the bus.
     */
    public static final int MAX_INLINE_CHARS = 20_000;

    /** Most characters of a call's arguments in a manifest or catalog line. */
    public static final int ARGS_SUMMARY_CHARS = 60;

    /** A run of the white space Go's strings.Fields-style folding in workspace-access treats as space: ASCII only. */
    private static final Pattern ASCII_SPACE_RUNS = Pattern.compile("[ \\t\\n\\x0B\\f\\r]+");

    /** All three kinds of result block, in the order they are removed. */
    private static final List<Pattern> RESULT_BLOCKS = List.of(RESULT_BLOCK, FAILED_RESULT_BLOCK, EMPTY_RESULT_BLOCK);

    private CallManifest() {}

    /** The text without any result block: a result, an error text or an empty result. */
    public static String withoutResultBlocks(String text) {
        String out = text;
        for (Pattern block : RESULT_BLOCKS) {
            out = block.matcher(out).replaceAll("");
        }
        return out;
    }

    /**
     * The markers a posted or read result must not hold: each would close a block or pose as one of
     * the discussion's own lines. The writer ({@link ToolCallLedger}) and the reader
     * (StoredResults) both refuse a result that holds one.
     */
    public static final List<String> RESULT_MARKERS = java.util.stream.Stream.concat(
            java.util.stream.Stream.of(CALL_PREFIX, UNSTORED_CALL_PREFIX, NOT_STORED_PREFIX, HEADER_PREFIX, RESULT_CLOSE),
            RESULT_OPENERS.stream()).toList();

    /** True when {@code text} holds a marker of the discussion's own format. */
    public static boolean holdsMarker(String text) {
        return RESULT_MARKERS.stream().anyMatch(text::contains);
    }

    /** The line, with its leading line break, that says {@code count} calls of an agent that stores nothing are not listed. */
    public static String omittedUnstoredLine(int count) {
        return "\n[" + count + " more calls not listed; this agent stores no results, so theirs are not available]";
    }

    /** The line, with its leading line break, that says {@code count} calls are not listed. */
    public static String omittedLine(int count) {
        return "\n[" + count + " more calls under the prefix]";
    }

    /**
     * The arguments on one line, cut at {@link #ARGS_SUMMARY_CHARS} characters. Arguments that
     * were shortened end with "... (args cut at N of M chars)", M being the length of the one-line
     * form, so a cut cannot be read as the real call. Square brackets become parentheses so a list
     * in the arguments cannot end the [CALL ...] line early. workspace-access's {@code argsSummary}
     * gives the same text.
     */
    public static String argsSummary(String arguments) {
        String one = oneLineArgs(arguments);
        int chars = one.codePointCount(0, one.length());
        if (chars <= ARGS_SUMMARY_CHARS) {
            return one;
        }
        return one.substring(0, one.offsetByCodePoints(0, ARGS_SUMMARY_CHARS)) + "... (args cut at "
                + ARGS_SUMMARY_CHARS + " of " + chars + " chars)";
    }

    /** The {@code args=...} part of a call line, in the manifest and in the catalog. */
    public static String argsField(String arguments) {
        return "args=" + argsSummary(arguments);
    }

    /** The text without one leading and one trailing ASCII space (runs are already folded to one). */
    static String withoutEdgeSpace(String text) {
        int start = text.startsWith(" ") ? 1 : 0;
        int end = text.length() > start && text.endsWith(" ") ? text.length() - 1 : text.length();
        return text.substring(start, end);
    }

    private static String oneLineArgs(String arguments) {
        String one = arguments == null ? ""
                : withoutEdgeSpace(ASCII_SPACE_RUNS.matcher(neutralize(arguments)).replaceAll(" "))
                        .replace('[', '(').replace(']', ')');
        return one.isEmpty() ? "{}" : one;
    }

    private static final Pattern CONTROL_CHARS = Pattern.compile("[\\p{Cc}\\u2028\\u2029]");

    /** The text with every control character (an escape, a line separator) turned into a space, so it cannot act on a terminal or end a line. */
    public static String neutralize(String text) {
        return CONTROL_CHARS.matcher(text).replaceAll(" ");
    }
}
