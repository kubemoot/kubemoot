package ai.kubemoot.agent.chat;

import java.util.List;

/**
 * One tool call as stored in the workspace: the object {@code key}, the tool, the call's
 * arguments as sent, the stored size, the row or item count ({@code -1} when unknown),
 * whether the result was a tool error, the stored format ({@code json}, {@code text-table} or
 * {@code text}) and the column or field names of a table or a JSON list.
 */
record ToolCallRecord(String key, String tool, String arguments, long bytes, int rows, boolean error,
                      String format, List<String> columns) {}
