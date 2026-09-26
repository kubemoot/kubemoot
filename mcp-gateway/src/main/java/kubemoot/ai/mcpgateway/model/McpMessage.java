package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.annotation.JsonInclude;

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

    public static McpMessage notification(String method) {
        return new McpMessage(JSON_RPC_VERSION, null, method, null, null, null);
    }

    public static McpMessage response(Object id, Object result) {
        return new McpMessage(JSON_RPC_VERSION, id, null, null, result, null);
    }

    public static McpMessage error(Object id, int code, String message) {
        return new McpMessage(JSON_RPC_VERSION, id, null, null, null, new McpError(code, message, null));
    }

    public record McpError(int code, String message, Object data) {}
}
