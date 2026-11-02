package ai.kubemoot.agent.chat;

import java.util.regex.Pattern;

/**
 * The text form of a tooler's manifest, shared by the code that writes it ({@link ToolCallLedger}),
 * the compute contract that looks for stored results in its input, and the coordinator that fills a
 * manifest in with the stored results before synthesis. One definition, so the writer and the
 * readers cannot drift.
 */
public final class CallManifest {

    /** Opens the header line: {@code [WORKSPACE agent=... calls=N errors=E prefix=...]}. */
    public static final String HEADER_PREFIX = "[WORKSPACE ";

    /** Opens a call line: {@code [CALL key=<key> tool=<tool> args=... bytes=... rows=... error=...]}. */
    public static final String CALL_PREFIX = "[CALL key=";

    /** Opens the closing line that says where the results are. */
    public static final String FOOTER_PREFIX = "The results are stored under these keys";

    /** A header line with its line break. */
    public static final Pattern HEADER_LINE = Pattern.compile("\\[WORKSPACE [^\\]]*+\\]\\n?");

    /** A call line: group 1 is the object key, group 2 the tool. */
    public static final Pattern CALL_LINE =
            Pattern.compile("\\[CALL key=([^\\]\\s]++) tool=([^\\]\\s]++)[^\\]]*+\\]");

    /** The closing line, up to its end. */
    public static final Pattern FOOTER_LINE =
            Pattern.compile("\\n?" + Pattern.quote(FOOTER_PREFIX) + "[^\\n]*+");

    private CallManifest() {}
}
