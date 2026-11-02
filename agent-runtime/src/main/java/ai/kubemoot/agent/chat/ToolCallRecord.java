package ai.kubemoot.agent.chat;

/**
 * One tool call as stored in the workspace: the object {@code key}, the tool, the call's
 * arguments as sent, the stored size, the row or item count ({@code -1} when unknown) and
 * whether the result was a tool error.
 */
record ToolCallRecord(String key, String tool, String arguments, long bytes, int rows, boolean error) {}
