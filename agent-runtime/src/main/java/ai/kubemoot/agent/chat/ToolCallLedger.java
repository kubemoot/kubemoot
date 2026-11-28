package ai.kubemoot.agent.chat;

import ai.kubemoot.agent.mcp.WorkspaceTools;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.OptionalLong;

/**
 * The per-call workspace objects of one agent's tool loop. Every tool call's result is written as
 * its own object, with headers that describe the call, and the agent's contribution carries a
 * manifest of those objects: the description of each file, and, for a result small enough for the
 * discussion ({@link DiscussionRoom}), the result itself in whole after its description. A larger
 * result is only in its file. The manifest is built in code from what was stored, so it is the same
 * text for the same calls.
 *
 * <p>Keys in the manifest are relative to the thread, {@code <agent>/<nnn>-<tool>}; the artifact
 * tools read them inside the thread the call runs in.
 *
 * <p>A call whose write failed has a line in the manifest that says it was not stored, so the crew
 * learns that the data existed and is not available.
 */
final class ToolCallLedger {

    /** What the loop knows about a call before its result is stored. */
    record Call(String tool, String callId, String arguments, Instant started, long durationMs) {}

    /** The descriptions stay under this size so they are posted inline whole. */
    static final int MAX_MANIFEST_CHARS = CallManifest.MAX_CHARS;
    /** Column or field names a call line lists when the manifest is too crowded to list them all. */
    static final int CROWDED_COLUMNS = 12;

    /** The store the results go to, or null for an agent that stores nothing: its calls are listed, not kept. */
    private final CallArtifactStore store;
    private final String threadId;
    private final String agent;
    private final String threadPrefix;
    private final int perResultChars;
    private int inlineRoomLeft;
    private final List<ToolCallRecord> calls = new ArrayList<>();

    /** A ledger whose readers' window is not known, so results are sized from {@link DiscussionRoom#UNKNOWN_WINDOW_CHARS}. */
    ToolCallLedger(CallArtifactStore store, String threadId, String agent) {
        this(store, threadId, agent, OptionalLong.empty());
    }

    /** {@code readerWindowTokens} is the smallest context window of the models that read the discussion. */
    ToolCallLedger(CallArtifactStore store, String threadId, String agent, OptionalLong readerWindowTokens) {
        this.store = store;
        this.threadId = threadId;
        this.agent = agent;
        this.threadPrefix = store == null ? "" : store.threadPrefix(threadId);
        this.perResultChars = DiscussionRoom.perResultChars(readerWindowTokens);
        this.inlineRoomLeft = DiscussionRoom.perContributionChars(readerWindowTokens);
    }

    /** A ledger for a loop, or null when there is no store or no discussion thread to store under. */
    static ToolCallLedger forLoop(CallArtifactStore store, String threadId, String agent,
                                  OptionalLong readerWindowTokens) {
        boolean usable = store != null && threadId != null && !threadId.isBlank();
        return usable ? new ToolCallLedger(store, threadId, agent, readerWindowTokens) : null;
    }

    /**
     * A ledger for an agent whose workspace access does not include storing: it lists its calls and
     * posts their results under the same room, and writes nothing. Null when there is no thread.
     */
    static ToolCallLedger unstoredForLoop(String threadId, String agent, OptionalLong readerWindowTokens) {
        boolean usable = threadId != null && !threadId.isBlank();
        return usable ? new ToolCallLedger(null, threadId, agent, readerWindowTokens) : null;
    }

    /**
     * Store one call's result as its own object. Returns the object key, or null when the write
     * failed. The body is the tool's own result text, cut at {@code maxChars}.
     */
    String recordCall(Call call, String result, boolean error, int maxChars) {
        String text = result == null ? "" : result;
        if (store == null) {
            return recordUnstored(call, text, error, maxChars);
        }
        boolean cut = text.length() > maxChars;
        byte[] body = (cut ? TextCut.truncate(text, maxChars) : text).getBytes(StandardCharsets.UTF_8);
        ToolResultShape shape = ToolResultShape.of(text);
        String key = store.reserveKey(threadId, agent, call.tool());
        if (key == null) {
            calls.add(ToolCallRecord.notStored(call.tool(), call.arguments(), error));
            return null;
        }
        Map<String, String> headers = headers(call, key, body.length, shape, error, cut);
        if (!store.put(threadId, agent, call.tool(), key, body, headers)) {
            calls.add(ToolCallRecord.notStored(call.tool(), call.arguments(), error));
            return null;
        }
        String working = storeWorking(call, key, maxChars);
        var outcome = new ToolCallRecord.Outcome(body.length, shape.rows(), error, storedType(shape, cut),
                cut ? List.of() : shape.columns());
        calls.add(ToolCallRecord.stored(key, working, call.tool(), call.arguments(), outcome,
                cut ? null : inlineOf(text, error)));
        return key;
    }

    /**
     * Store the call's working, its arguments exactly as the model sent them (the program of an
     * execute_code call, the query, the parameters), beside its result under
     * {@link CallManifest#workingKeyOf}, cut at the same {@code maxChars} as the result. The object
     * carries no {@link CallHeaders#OPERATION_NAME}, so the catalogs and the readers of a tool's
     * calls as one table never take it for a result. Returns the key, or null when the write failed.
     */
    private String storeWorking(Call call, String resultKey, int maxChars) {
        String args = call.arguments() == null ? "" : call.arguments();
        boolean cut = args.length() > maxChars;
        byte[] body = (cut ? TextCut.truncate(args, maxChars) : args).getBytes(StandardCharsets.UTF_8);
        String key = CallManifest.workingKeyOf(resultKey);
        var h = new LinkedHashMap<String, String>();
        h.put(CallHeaders.WORKING_OF, resultKey);
        h.put(CallHeaders.TOOL_NAME, call.tool());
        h.put(CallHeaders.TOOL_CALL_ID, callId(call, resultKey));
        h.put(CallHeaders.AGENT_NAME, agent);
        h.put(CallHeaders.STARTED, call.started().toString());
        h.put(CallHeaders.BYTES, Integer.toString(body.length));
        h.put(CallHeaders.CONTENT_TYPE, cut || args.isBlank() ? ResultType.TEXT : ResultType.of(args));
        if (cut) {
            h.put(CallHeaders.TRUNCATED, "true");
        }
        return store.put(threadId, agent, call.tool(), key, body, h) ? key : null;
    }

    /** List a call of an agent that stores nothing: its size and shape, and its result when it fits. Returns null: there is no key. */
    private String recordUnstored(Call call, String text, boolean error, int maxChars) {
        ToolResultShape shape = ToolResultShape.of(text);
        boolean cut = text.length() > maxChars;
        calls.add(ToolCallRecord.unstored(call.tool(), call.arguments(), text.getBytes(StandardCharsets.UTF_8).length,
                shape.rows(), error, storedType(shape, cut), cut ? null : inlineOf(text, error)));
        return null;
    }

    /**
     * The result as the block that posts it in the discussion, or null when it stays in its file
     * only. A successful call with an empty result always gets the empty block, whatever the room.
     * A result goes in whole or not at all: it must fit one result's share of the room and
     * what is left of the contribution's, and must not hold the closing marker itself. The room it
     * uses is taken from the contribution's.
     */
    private String inlineOf(String text, boolean error) {
        if (text.isBlank()) {
            return blankBlock(error);
        }
        if (text.length() > perResultChars || CallManifest.holdsMarker(text)) {
            return null;
        }
        String open = error ? CallManifest.FAILED_RESULT_OPEN : CallManifest.RESULT_OPEN;
        String block = open + "\n" + text + "\n" + CallManifest.RESULT_CLOSE;
        if (block.length() + 1 > inlineRoomLeft) {
            return null;
        }
        inlineRoomLeft -= block.length() + 1;
        return block;
    }

    /**
     * The block of a result with no text: the empty block for a call that succeeded, and for a call
     * that failed of an agent that stores nothing, so each of its lines has a block. A stored call
     * that failed with no text has none: its file is empty.
     */
    private String blankBlock(boolean error) {
        if (!error) {
            return CallManifest.EMPTY_RESULT_TEXT;
        }
        return store == null ? CallManifest.FAILED_NO_TEXT_TEXT : null;
    }

    /** The type of the stored bytes: a result cut at the size cap no longer holds what it was, so it is text. */
    private static String storedType(ToolResultShape shape, boolean cut) {
        return cut ? ResultType.TEXT : shape.type();
    }

    private Map<String, String> headers(Call call, String key, int bytes, ToolResultShape shape,
                                        boolean error, boolean cut) {
        var h = new LinkedHashMap<String, String>();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, call.tool());
        h.put(CallHeaders.TOOL_CALL_ID, callId(call, key));
        h.put(CallHeaders.TOOL_CALL_ARGUMENTS,
                TextCut.truncate(call.arguments(), CallHeaders.MAX_ARGUMENTS_CHARS));
        h.put(CallHeaders.AGENT_NAME, agent);
        h.put(CallHeaders.STARTED, call.started().toString());
        h.put(CallHeaders.DURATION_MS, Long.toString(call.durationMs()));
        h.put(CallHeaders.ERROR, Boolean.toString(error));
        if (error) {
            h.put(CallHeaders.ERROR_TYPE, CallHeaders.ERROR_TYPE_TOOL);
        }
        h.put(CallHeaders.BYTES, Integer.toString(bytes));
        if (shape.rows() >= 0) {
            h.put(CallHeaders.ROWS, Integer.toString(shape.rows()));
        }
        h.put(CallHeaders.CONTENT_TYPE, storedType(shape, cut));
        if (cut) {
            h.put(CallHeaders.TRUNCATED, "true");
        }
        return h;
    }

    /** The model's call id, or the last part of the key (call number and tool) when it sent none. */
    private static String callId(Call call, String key) {
        if (call.callId() != null && !call.callId().isBlank()) {
            return call.callId();
        }
        return key.substring(key.lastIndexOf('/') + 1);
    }

    /** True when the agent made a tool call, so its contribution has a manifest to carry. */
    boolean holdsOutput() {
        return !calls.isEmpty();
    }

    /** Number of calls recorded, stored or not. */
    int size() {
        return calls.size();
    }

    /**
     * The file descriptions of the agent's contribution: a header line, one line per call (each
     * followed by its result when the result was small enough to post), and one line saying where
     * the results and the workings are. A call line names its working's key ({@code call=}) beside
     * the summary of its arguments, and both are dropped together when the room is short: the
     * closing line says how a working's key is made from the result's. Arguments are summarized,
     * then dropped, then the last calls without data are replaced by a count, so the descriptions,
     * with the empty-result blocks (which are protocol text and always posted), never pass
     * {@link #MAX_MANIFEST_CHARS}; results with data come on top, within the room of the
     * contribution ({@link CallManifest#MAX_INLINE_CHARS}).
     */
    String manifest() {
        String prefix = prefix();
        long errors = calls.stream().filter(ToolCallRecord::error).count();
        String head = CallManifest.HEADER_PREFIX + "agent=" + agent + " calls=" + calls.size() + " errors=" + errors
                + (store == null ? " stored=no" : " prefix=" + prefix) + "]\n";
        String keyHint = prefix.isEmpty() ? "." : "; the key " + prefix + "*-<tool> reads every call of one tool as one table.";
        String foot = store == null ? "" : "\n" + CallManifest.FOOTER_PREFIX + "; a result not shown above is only in its file, and "
                + CallManifest.WORKING_NOTE + ". Read a key with "
                + WorkspaceTools.QUERY + " (SQL; the columns are listed on the call line, and "
                + WorkspaceTools.DESCRIBE + " lists every one), "
                + WorkspaceTools.HEAD + ", " + WorkspaceTools.TAIL + ", " + WorkspaceTools.GREP + ", or "
                + "execute_code over the file " + WorkspaceTools.MOUNT + "/<key>"
                + keyHint;
        int room = MAX_MANIFEST_CHARS - head.length() - foot.length();
        return head + lines(room) + foot;
    }

    /**
     * Every call's line with what fits: arguments and every column name, then every column name
     * alone, then the first {@link #CROWDED_COLUMNS}, then none, and only then calls replaced by a
     * count (the last ones without a posted result first, so a posted result keeps its line).
     */
    private String lines(int room) {
        for (Detail detail : DETAILS) {
            if (joined(calls, detail.withArgs(), detail.columns(), Posted.EMPTY_ONLY).length() <= room) {
                return joined(calls, detail.withArgs(), detail.columns(), Posted.ALL);
            }
        }
        var shown = new ArrayList<>(calls);
        while (!shown.isEmpty() && joined(shown, false, 0, Posted.EMPTY_ONLY).length()
                + omitted(calls.size() - shown.size()).length() > room) {
            shown.remove(lastToDrop(shown));
        }
        return joined(shown, false, 0, Posted.ALL) + omitted(calls.size() - shown.size());
    }

    /** How much of a call line is shown: whether it has the arguments, and how many column names it lists. */
    private record Detail(boolean withArgs, int columns) {}

    /** From the fullest call line to the barest. */
    private static final List<Detail> DETAILS = List.of(
            new Detail(true, ColumnNames.MAX_LISTED), new Detail(false, ColumnNames.MAX_LISTED),
            new Detail(false, CROWDED_COLUMNS), new Detail(false, 0));

    /** Which posted results {@link #joined} adds after the call lines. */
    private enum Posted {
        /** Only the empty-result blocks, which count against the descriptions' size. */
        EMPTY_ONLY,
        /** Every posted result. */
        ALL;

        boolean shows(ToolCallRecord c) {
            return this == ALL || this == EMPTY_ONLY && !c.hasData();
        }
    }

    /** The index of the last call that holds no data (an empty or unposted result), or of the last call when every one holds data. */
    private static int lastToDrop(List<ToolCallRecord> shown) {
        for (int i = shown.size() - 1; i >= 0; i--) {
            if (!shown.get(i).hasData()) {
                return i;
            }
        }
        return shown.size() - 1;
    }

    private String omitted(int count) {
        if (count == 0) {
            return "";
        }
        return store == null ? CallManifest.omittedUnstoredLine(count) : CallManifest.omittedLine(count);
    }

    private String joined(List<ToolCallRecord> shown, boolean withArgs, int columnLimit, Posted posted) {
        var sb = new StringBuilder();
        for (int i = 0; i < shown.size(); i++) {
            ToolCallRecord c = shown.get(i);
            if (i > 0) {
                sb.append('\n');
            }
            sb.append(line(c, withArgs, columnLimit));
            if (c.hasInline() && posted.shows(c)) {
                sb.append('\n').append(c.inline());
            }
        }
        return sb.toString();
    }

    private String line(ToolCallRecord c, boolean withArgs, int columnLimit) {
        if (c.storeOff() && c.hasInline()) {
            return callLine(new StringBuilder(CallManifest.UNSTORED_CALL_PREFIX).append(c.tool()), c, withArgs)
                    .append(" stored=no]").toString();
        }
        if (!c.stored()) {
            return CallManifest.NOT_STORED_PREFIX + c.tool() + (withArgs ? " " + CallManifest.argsField(c.arguments()) : "")
                    + " error=" + c.error() + " - "
                    + (c.storeOff() ? CallManifest.NOT_KEPT_NOTE : CallManifest.WRITE_FAILED_NOTE) + "]";
        }
        return storedLine(c, withArgs, columnLimit);
    }

    /** The line of a stored call: its key, its working's key beside its arguments, and its shape. */
    private String storedLine(ToolCallRecord c, boolean withArgs, int columnLimit) {
        var sb = new StringBuilder(CallManifest.CALL_PREFIX).append(shortKey(c.key())).append(" tool=").append(c.tool());
        if (withArgs) {
            sb.append(' ').append(CallManifest.WORKING_FIELD)
                    .append(c.workingKey() == null ? CallManifest.WORKING_UNAVAILABLE : shortKey(c.workingKey()));
        }
        callLine(sb, c, withArgs);
        if (columnLimit > 0 && !c.columns().isEmpty()) {
            sb.append(ResultType.isJson(c.type()) ? " fields=" : " columns=").append(ColumnNames.list(c.columns(), columnLimit));
        }
        return sb.append(']').toString();
    }

    /** What every call line has after its opening: the arguments, the type, the size, the rows and whether it failed. */
    private static StringBuilder callLine(StringBuilder sb, ToolCallRecord c, boolean withArgs) {
        if (withArgs) {
            sb.append(' ').append(CallManifest.argsField(c.arguments()));
        }
        sb.append(" type=").append(c.type());
        sb.append(" bytes=").append(c.bytes())
                .append(" rows=").append(c.rows() >= 0 ? Integer.toString(c.rows()) : "unknown")
                .append(" error=").append(c.error());
        return sb;
    }

    /** The key relative to the thread: {@code <agent>/<nnn>-<tool>}. */
    private String shortKey(String key) {
        return key.startsWith(threadPrefix) ? key.substring(threadPrefix.length()) : key;
    }

    /** The key prefix shared by this agent's calls in the thread, relative to the thread: {@code <agent>/}. */
    String prefix() {
        return calls.stream().filter(ToolCallRecord::stored).findFirst()
                .map(c -> shortKey(c.key()))
                .map(k -> k.substring(0, k.lastIndexOf('/') + 1)).orElse("");
    }
}
