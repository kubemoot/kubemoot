package ai.kubemoot.agent.chat;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * The per-call workspace objects of one tooler's tool loop. Each tool call's result is written
 * as its own object, with headers that describe the call, and the tooler's contribution becomes
 * a short manifest of those objects instead of the results glued together. The manifest is built
 * in code from what was stored, so it is the same text for the same calls.
 *
 * <p>A call whose write failed is not in the manifest, and {@link #complete()} is then false: the
 * caller posts the glued output as before, so a storage failure never loses data.
 */
final class ToolCallLedger {

    /** What the loop knows about a call before its result is stored. */
    record Call(String tool, String callId, String arguments, Instant started, long durationMs) {}

    /** The manifest stays under the runtime's spill threshold so it is posted inline. */
    static final int MAX_MANIFEST_CHARS = 4_000;
    /** Most characters of a call's arguments in a manifest line. */
    static final int ARGS_SUMMARY_CHARS = 60;

    private final CallArtifactStore store;
    private final String threadId;
    private final String agent;
    private final List<ToolCallRecord> calls = new ArrayList<>();
    private boolean everyWriteWorked = true;

    ToolCallLedger(CallArtifactStore store, String threadId, String agent) {
        this.store = store;
        this.threadId = threadId;
        this.agent = agent;
    }

    /** A ledger for a loop, or null when there is no store, no thread or the loop is not a tooler's. */
    static ToolCallLedger forLoop(boolean enabled, CallArtifactStore store, String threadId, String agent) {
        boolean usable = enabled && store != null && threadId != null && !threadId.isBlank();
        return usable ? new ToolCallLedger(store, threadId, agent) : null;
    }

    /**
     * Store one call's result as its own object. Returns the object key, or null when the write
     * failed. The body is the tool's own result text, cut at {@code maxChars}.
     */
    String record(Call call, String result, boolean error, int maxChars) {
        String text = result == null ? "" : result;
        boolean cut = text.length() > maxChars;
        byte[] body = (cut ? ChatService.truncate(text, maxChars) : text).getBytes(StandardCharsets.UTF_8);
        ToolResultShape shape = ToolResultShape.of(text);
        String key = store.reserveKey(threadId, agent, call.tool());
        Map<String, String> headers = headers(call, key, body.length, shape, error, cut);
        if (!store.put(threadId, agent, call.tool(), key, body, headers)) {
            everyWriteWorked = false;
            return null;
        }
        calls.add(new ToolCallRecord(key, call.tool(), call.arguments(), body.length, shape.rows(), error));
        return key;
    }

    private Map<String, String> headers(Call call, String key, int bytes, ToolResultShape shape,
                                        boolean error, boolean cut) {
        var h = new LinkedHashMap<String, String>();
        h.put(CallHeaders.OPERATION_NAME, CallHeaders.OPERATION_EXECUTE_TOOL);
        h.put(CallHeaders.TOOL_NAME, call.tool());
        h.put(CallHeaders.TOOL_CALL_ID, callId(call, key));
        h.put(CallHeaders.TOOL_CALL_ARGUMENTS,
                ChatService.truncate(call.arguments(), CallHeaders.MAX_ARGUMENTS_CHARS));
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

    /** True when at least one call is stored and every write worked. */
    boolean complete() {
        return everyWriteWorked && !calls.isEmpty();
    }

    /** True when any call was stored, so the tooler holds output. */
    boolean holdsOutput() {
        return !calls.isEmpty();
    }

    /** Number of calls stored. */
    int size() {
        return calls.size();
    }

    /**
     * The tooler's contribution: a header line, one line per call, and one line saying where the
     * results are. Arguments are summarized, then dropped, then the oldest lines are replaced by
     * a count, so the text never passes {@link #MAX_MANIFEST_CHARS}.
     */
    String manifest() {
        String prefix = prefix();
        long errors = calls.stream().filter(ToolCallRecord::error).count();
        String head = "[WORKSPACE agent=" + agent + " calls=" + calls.size() + " errors=" + errors
                + " prefix=" + prefix + "]\n";
        String foot = "\nThe results are stored under these keys and are not shown here. Read a key with "
                + "workspace_query (SQL) or the artifact_* tools; the key " + prefix
                + "*-<tool> reads every call of one tool as one table.";
        int room = MAX_MANIFEST_CHARS - head.length() - foot.length();
        return head + lines(room) + foot;
    }

    private String lines(int room) {
        String withArgs = joined(calls.size(), true);
        if (withArgs.length() <= room) {
            return withArgs;
        }
        String bare = joined(calls.size(), false);
        if (bare.length() <= room) {
            return bare;
        }
        int shown = calls.size();
        String cut = joined(shown, false);
        while (shown > 0 && cut.length() + omitted(calls.size() - shown).length() > room) {
            shown--;
            cut = joined(shown, false);
        }
        return cut + omitted(calls.size() - shown);
    }

    private static String omitted(int count) {
        return count == 0 ? "" : "\n[" + count + " more calls under the prefix]";
    }

    private String joined(int count, boolean withArgs) {
        var sb = new StringBuilder();
        for (int i = 0; i < count; i++) {
            if (i > 0) {
                sb.append('\n');
            }
            sb.append(line(calls.get(i), withArgs));
        }
        return sb.toString();
    }

    private static String line(ToolCallRecord c, boolean withArgs) {
        var sb = new StringBuilder("[CALL key=").append(c.key()).append(" tool=").append(c.tool());
        if (withArgs) {
            sb.append(" args=").append(summary(c.arguments()));
        }
        sb.append(" bytes=").append(c.bytes())
                .append(" rows=").append(c.rows() >= 0 ? Integer.toString(c.rows()) : "unknown")
                .append(" error=").append(c.error()).append(']');
        return sb.toString();
    }

    /** The arguments on one line, cut at {@link #ARGS_SUMMARY_CHARS}. */
    static String summary(String arguments) {
        String one = arguments == null ? "" : arguments.replaceAll("\\s+", " ").strip();
        if (one.isEmpty()) {
            return "{}";
        }
        return one.length() <= ARGS_SUMMARY_CHARS ? one : one.substring(0, ARGS_SUMMARY_CHARS) + "...";
    }

    /** The key prefix shared by this agent's calls in the thread: everything up to the last slash. */
    String prefix() {
        if (calls.isEmpty()) {
            return "";
        }
        String key = calls.get(0).key();
        return key.substring(0, key.lastIndexOf('/') + 1);
    }
}
