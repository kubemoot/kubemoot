package ai.kubemoot.agent.chat;

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
    /** Most characters of a call's arguments in a manifest line. */
    static final int ARGS_SUMMARY_CHARS = CallManifest.ARGS_SUMMARY_CHARS;
    /** Most column or field names of one call in a manifest line, and the most characters of one name. */
    static final int MAX_COLUMNS_SHOWN = 12;
    static final int MAX_NAME_CHARS = 30;

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
        this.threadPrefix = store.threadPrefix(threadId);
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
     * Store one call's result as its own object. Returns the object key, or null when the write
     * failed. The body is the tool's own result text, cut at {@code maxChars}.
     */
    String record(Call call, String result, boolean error, int maxChars) {
        String text = result == null ? "" : result;
        boolean cut = text.length() > maxChars;
        byte[] body = (cut ? TextCut.truncate(text, maxChars) : text).getBytes(StandardCharsets.UTF_8);
        ToolResultShape shape = ToolResultShape.of(text);
        String key = store.reserveKey(threadId, agent, call.tool());
        Map<String, String> headers = headers(call, key, body.length, shape, error, cut);
        if (!store.put(threadId, agent, call.tool(), key, body, headers)) {
            calls.add(ToolCallRecord.notStored(call.tool(), call.arguments(), error));
            return null;
        }
        String format = cut && shape.json() ? CallHeaders.FORMAT_TEXT : shape.format();
        calls.add(new ToolCallRecord(key, call.tool(), call.arguments(), body.length, shape.rows(), error,
                format, cut ? List.of() : shape.columns(), cut ? null : inlineOf(text, error)));
        return key;
    }

    /**
     * The result as the block that posts it in the discussion, or null when it stays in its file
     * only. A result goes in whole or not at all: it must fit one result's share of the room and
     * what is left of the contribution's, and must not hold the closing marker itself. The room it
     * uses is taken from the contribution's.
     */
    private String inlineOf(String text, boolean error) {
        if (text.isBlank() || text.length() > perResultChars || text.contains(CallManifest.RESULT_CLOSE)
                || text.contains(CallManifest.CALL_PREFIX)) {
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
        h.put(CallHeaders.FORMAT, shape.json() && !cut ? CallHeaders.FORMAT_JSON : CallHeaders.FORMAT_TEXT);
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
     * the results are. Arguments are summarized, then dropped, then the last calls without a result
     * are replaced by a count, so the descriptions never pass {@link #MAX_MANIFEST_CHARS}.
     */
    String manifest() {
        String prefix = prefix();
        long errors = calls.stream().filter(ToolCallRecord::error).count();
        String head = CallManifest.HEADER_PREFIX + "agent=" + agent + " calls=" + calls.size() + " errors=" + errors
                + " prefix=" + prefix + "]\n";
        String foot = "\n" + CallManifest.FOOTER_PREFIX + "; a result not shown above is only in its file. Read a key with "
                + "workspace_query (SQL; the columns are listed on the call line), the artifact_* tools, or "
                + "execute_code over the file /artifacts/<key>"
                + (prefix.isEmpty() ? "." : "; the key " + prefix + "*-<tool> reads every call of one tool as one table.");
        int room = MAX_MANIFEST_CHARS - head.length() - foot.length();
        return head + lines(room) + foot;
    }

    /**
     * Every call's line with what fits: arguments and column names, then column names alone, then
     * neither, and only then calls replaced by a count (the last ones without a posted result
     * first, so a posted result keeps its line).
     */
    private String lines(int room) {
        for (boolean[] detail : new boolean[][] {{true, true}, {false, true}, {false, false}}) {
            if (joined(calls, detail[0], detail[1], false).length() <= room) {
                return joined(calls, detail[0], detail[1], true);
            }
        }
        var shown = new ArrayList<>(calls);
        while (!shown.isEmpty() && joined(shown, false, false, false).length()
                + omitted(calls.size() - shown.size()).length() > room) {
            shown.remove(lastToDrop(shown));
        }
        return joined(shown, false, false, true) + omitted(calls.size() - shown.size());
    }

    /** The index of the last call without a posted result, or of the last call when every one has a result. */
    private static int lastToDrop(List<ToolCallRecord> shown) {
        for (int i = shown.size() - 1; i >= 0; i--) {
            if (!shown.get(i).hasInline()) {
                return i;
            }
        }
        return shown.size() - 1;
    }

    private static String omitted(int count) {
        return count == 0 ? "" : CallManifest.omittedLine(count);
    }

    private String joined(List<ToolCallRecord> shown, boolean withArgs, boolean withColumns, boolean withResults) {
        var sb = new StringBuilder();
        for (int i = 0; i < shown.size(); i++) {
            ToolCallRecord c = shown.get(i);
            if (i > 0) {
                sb.append('\n');
            }
            sb.append(line(c, withArgs, withColumns));
            if (withResults && c.hasInline()) {
                sb.append('\n').append(c.inline());
            }
        }
        return sb.toString();
    }

    private String line(ToolCallRecord c, boolean withArgs, boolean withColumns) {
        if (!c.stored()) {
            return CallManifest.NOT_STORED_PREFIX + c.tool() + (withArgs ? " args=" + summary(c.arguments()) : "")
                    + " error=" + c.error() + " - the result could not be written to the workspace and is not available]";
        }
        var sb = new StringBuilder(CallManifest.CALL_PREFIX).append(shortKey(c.key())).append(" tool=").append(c.tool());
        if (withArgs) {
            sb.append(" args=").append(summary(c.arguments()));
        }
        sb.append(" format=").append(c.format());
        sb.append(" bytes=").append(c.bytes())
                .append(" rows=").append(c.rows() >= 0 ? Integer.toString(c.rows()) : "unknown")
                .append(" error=").append(c.error());
        if (withColumns && !c.columns().isEmpty()) {
            sb.append(CallHeaders.FORMAT_JSON.equals(c.format()) ? " fields=" : " columns=").append(columnList(c.columns()));
        }
        return sb.append(']').toString();
    }

    /** The key relative to the thread: {@code <agent>/<nnn>-<tool>}. */
    private String shortKey(String key) {
        return key.startsWith(threadPrefix) ? key.substring(threadPrefix.length()) : key;
    }

    /**
     * The names, comma separated: at most {@link #MAX_COLUMNS_SHOWN} of them, each cut at
     * {@link #MAX_NAME_CHARS}, then a count of the rest. Square brackets become parentheses so a name
     * cannot end the [CALL ...] line early.
     */
    static String columnList(List<String> names) {
        var shown = new ArrayList<String>();
        for (String name : names.subList(0, Math.min(names.size(), MAX_COLUMNS_SHOWN))) {
            String one = name.replace('[', '(').replace(']', ')');
            shown.add(one.length() <= MAX_NAME_CHARS ? one : one.substring(0, MAX_NAME_CHARS) + "...");
        }
        int more = names.size() - shown.size();
        return String.join(",", shown) + (more > 0 ? ",+" + more + "more" : "");
    }

    /** The arguments on one line, cut at {@link #ARGS_SUMMARY_CHARS}. */
    static String summary(String arguments) {
        return CallManifest.argsSummary(arguments);
    }

    /** The key prefix shared by this agent's calls in the thread, relative to the thread: {@code <agent>/}. */
    String prefix() {
        return calls.stream().filter(ToolCallRecord::stored).findFirst()
                .map(c -> shortKey(c.key()))
                .map(k -> k.substring(0, k.lastIndexOf('/') + 1)).orElse("");
    }
}
