package kubemoot.ai.mcpgateway.service;

import org.springframework.core.io.buffer.DataBufferLimitException;

/**
 * The gateway's cap on one MCP server reply and the tool error that stands in for a reply over it.
 * The message names the size (when the server declared it) and the limit, so the agent sees a
 * clear failure instead of a cut or garbled result.
 */
final class ResponseLimit {

    /** JSON-RPC code of the error; the mcp-bridge uses the same code for the same condition. */
    static final int ERROR_CODE = -32000;
    static final String PROPERTY = "mcp.gateway.max-response-size";

    private ResponseLimit() {
    }

    /** True when the failure is the reply exceeding the in-memory limit, however deeply wrapped. */
    static boolean isExceeded(Throwable error) {
        for (Throwable t = error; t != null; t = t.getCause() == t ? null : t.getCause()) {
            if (t instanceof DataBufferLimitException) {
                return true;
            }
        }
        return false;
    }

    /** True when the declared length is known and over the limit. */
    static boolean isOver(long declaredLength, int limitBytes) {
        return declaredLength > limitBytes;
    }

    /** A log line cause for a failed read: names the setting when the limit was the cause. */
    static String describe(Throwable error, int limitBytes) {
        if (isExceeded(error)) {
            return "the reply was over the " + limitBytes + " byte limit (" + PROPERTY + ")";
        }
        return error.getMessage();
    }

    /** The error text; a negative size means the server did not declare one. */
    static String message(String serverName, long sizeBytes, int limitBytes) {
        String size = sizeBytes >= 0
            ? "was " + sizeBytes + " bytes"
            : "was larger than the limit and its full size is unknown";
        return "The tool result from " + serverName + " " + size + ", over the gateway limit of "
            + limitBytes + " bytes (" + PROPERTY + ") and was not delivered. "
            + "Ask for less: filter, select specific fields, or request a shorter range.";
    }
}
