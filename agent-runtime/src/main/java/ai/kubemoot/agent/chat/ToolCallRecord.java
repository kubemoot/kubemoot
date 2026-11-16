package ai.kubemoot.agent.chat;

import java.util.List;

/**
 * One tool call as stored in the workspace: the object {@code key}, the tool, the call's
 * arguments as sent, the stored size, the row or item count ({@code -1} when unknown),
 * whether the result was a tool error, the stored content type ({@link ResultType}) and the column or field names of a table or a JSON list. {@code inline} is the
 * whole result when it is small enough to be posted in the discussion as well, otherwise null. A
 * call that could not be written has no key.
 */
record ToolCallRecord(String key, String tool, String arguments, long bytes, int rows, boolean error,
                      String type, List<String> columns, String inline, boolean storeOff) {

    /** A call stored under {@code key}. */
    ToolCallRecord(String key, String tool, String arguments, long bytes, int rows, boolean error,
                   String type, List<String> columns, String inline) {
        this(key, tool, arguments, bytes, rows, error, type, columns, inline, false);
    }

    /** A call whose agent stores no results: it has no key, and its result is posted when it fits. */
    static ToolCallRecord unstored(String tool, String arguments, long bytes, int rows, boolean error,
                                   String type, String inline) {
        return new ToolCallRecord(null, tool, arguments, bytes, rows, error, type, List.of(), inline, true);
    }

    /** A call whose result could not be written: it has no key, and the manifest says so. */
    static ToolCallRecord notStored(String tool, String arguments, boolean error) {
        return new ToolCallRecord(null, tool, arguments, 0, -1, error, ResultType.TEXT, List.of(), null);
    }

    /** True when the call has a stored object under its key. */
    boolean stored() {
        return key != null;
    }

    boolean hasInline() {
        return inline != null;
    }

    /** True when a result with data is posted: a posted result that is not the empty-result block. */
    boolean hasData() {
        return hasInline() && !CallManifest.EMPTY_RESULT_TEXT.equals(inline);
    }
}
