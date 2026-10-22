package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.annotation.JsonInclude;

import java.util.LinkedHashMap;
import java.util.Map;

@JsonInclude(JsonInclude.Include.NON_NULL)
public record McpMessage(
    String jsonrpc,
    Object id,
    String method,
    Map<String, Object> params,
    Object result,
    McpError error
) {
    public static final String JSON_RPC_VERSION = "2.0";

    public static McpMessage request(Object id, String method, Map<String, Object> params) {
        return new McpMessage(JSON_RPC_VERSION, id, method, params, null, null);
    }

    /**
     * A tools/call request. The caller's {@code _meta} (MCP's per-request metadata, such as
     * the requester's time zone and date) rides in the params unchanged; a call without one
     * carries no {@code _meta}.
     */
    public static McpMessage toolsCall(Object id, String toolName, Map<String, Object> arguments,
                                       Object meta) {
        var params = new LinkedHashMap<String, Object>();
        params.put("name", toolName);
        params.put("arguments", arguments);
        var forwarded = RequesterMeta.sanitize(meta);
        if (!forwarded.isEmpty()) {
            params.put("_meta", forwarded);
        }
        return request(id, "tools/call", params);
    }

    public static McpMessage notification(String method) {
        return new McpMessage(JSON_RPC_VERSION, null, method, null, null, null);
    }

    public static McpMessage response(Object id, Object result) {
        return new McpMessage(JSON_RPC_VERSION, id, null, null, result, null);
    }

    public static McpMessage error(Object id, int code, String message) {
        return new McpMessage(JSON_RPC_VERSION, id, null, null, null, new McpError(code, message, null));
    }

    /** The same message with a different JSON-RPC id. */
    public McpMessage withId(Object newId) {
        return new McpMessage(jsonrpc, newId, method, params, result, error);
    }

    /** The same message with different params. */
    public McpMessage withParams(Map<String, Object> newParams) {
        return new McpMessage(jsonrpc, id, method, newParams, result, error);
    }

    public record McpError(int code, String message, Object data) {}
}
