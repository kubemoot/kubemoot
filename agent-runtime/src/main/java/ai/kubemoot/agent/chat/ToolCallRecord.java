package ai.kubemoot.agent.chat;

import java.util.List;

/**
 * One tool call as stored in the workspace: the object {@code key}, the tool, the call's
 * arguments as sent, the stored size, the row or item count ({@code -1} when unknown),
 * whether the result was a tool error, the stored format ({@code json}, {@code text-table} or
 * {@code text}) and the column or field names of a table or a JSON list. A call that could not be
 * written has no key.
 */
record ToolCallRecord(String key, String tool, String arguments, long bytes, int rows, boolean error,
                      String format, List<String> columns) {

    /** A call whose result could not be written: it has no key, and the manifest says so. */
    static ToolCallRecord notStored(String tool, String arguments, boolean error) {
        return new ToolCallRecord(null, tool, arguments, 0, -1, error, CallHeaders.FORMAT_TEXT, List.of());
    }

    boolean stored() {
        return key != null;
    }
}
