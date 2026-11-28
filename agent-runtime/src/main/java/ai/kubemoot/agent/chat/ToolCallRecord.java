package ai.kubemoot.agent.chat;

import java.util.List;

/**
 * One tool call of an agent's loop: the object {@code key} (only a stored call has one), the tool,
 * the call's arguments as sent, the size, the row or item count ({@code -1} when unknown), whether
 * the result was a tool error, the content type ({@link ResultType}) and the column or field names
 * of a table or a JSON list. {@code workingKey} is the key of the call's stored arguments (its
 * working), null when the call or its working was not stored. {@code inline} is the whole result when it is small enough to be
 * posted in the discussion, otherwise null. {@code storage} says whether the result is in the
 * workspace.
 */
record ToolCallRecord(Storage storage, String key, String workingKey, String tool, String arguments, long bytes,
                      int rows, boolean error, String type, List<String> columns, String inline) {

    /** Where the result of a call is. */
    enum Storage {
        /** In the workspace, under the call's key. */
        STORED,
        /** The write failed, so the result is nowhere. */
        WRITE_FAILED,
        /** The runtime has no workspace store, so the result is only what was posted. */
        NOT_STORED_BY_DESIGN
    }

    /** What a stored call returned: its size, its row count, whether it failed, its type and its columns. */
    record Outcome(long bytes, int rows, boolean error, String type, List<String> columns) {}

    /** A call stored under {@code key}, its working under {@code workingKey} (null when that write failed). */
    static ToolCallRecord stored(String key, String workingKey, String tool, String arguments, Outcome outcome,
                                 String inline) {
        return new ToolCallRecord(Storage.STORED, key, workingKey, tool, arguments, outcome.bytes(), outcome.rows(),
                outcome.error(), outcome.type(), outcome.columns(), inline);
    }

    /** A call whose agent stores no results: it has no key, and its result is posted when it fits. */
    static ToolCallRecord unstored(String tool, String arguments, long bytes, int rows, boolean error,
                                   String type, String inline) {
        return new ToolCallRecord(Storage.NOT_STORED_BY_DESIGN, null, null, tool, arguments, bytes, rows, error, type,
                List.of(), inline);
    }

    /** A call whose result could not be written: it has no key, and the manifest says so. */
    static ToolCallRecord notStored(String tool, String arguments, boolean error) {
        return new ToolCallRecord(Storage.WRITE_FAILED, null, null, tool, arguments, 0, -1, error, ResultType.TEXT,
                List.of(), null);
    }

    /** True when the call has a stored object under its key. */
    boolean stored() {
        return storage == Storage.STORED;
    }

    /** True when the agent stores nothing, so the call has no key by design. */
    boolean storeOff() {
        return storage == Storage.NOT_STORED_BY_DESIGN;
    }

    boolean hasInline() {
        return inline != null;
    }

    /** True when a result with data is posted: a posted result that is not one of the fixed no-data blocks. */
    boolean hasData() {
        return hasInline() && !CallManifest.NO_DATA_BLOCKS.contains(inline);
    }
}
