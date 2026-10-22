package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.controller.ToolController;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;

/** The caller's {@code _meta} reaches the upstream tools/call unchanged, and is absent when the caller sent none. */
class McpMessageToolsCallTest {

    private static final Map<String, Object> ARGS = Map.of("topic", "kubernetes");

    @Test
    void metaIsForwardedUnchanged() {
        Map<String, Object> meta = Map.of("timeZone", "America/Los_Angeles", "requestDate", "2026-10-24");
        var message = McpMessage.toolsCall(7, "find_released", ARGS, meta);
        assertEquals("tools/call", message.method());
        assertEquals(meta, message.params().get("_meta"));
        assertEquals(ARGS, message.params().get("arguments"));
    }

    @Test
    void noMetaMeansNoMetaKey() {
        assertFalse(McpMessage.toolsCall(7, "find_released", ARGS, null).params().containsKey("_meta"));
    }

    @Test
    void emptyMetaMeansNoMetaKey() {
        assertFalse(McpMessage.toolsCall(7, "find_released", ARGS, Map.of()).params().containsKey("_meta"));
    }

    @Test
    void restBodyCarriesMetaUnderTheMcpKey() throws Exception {
        var body = "{\"arguments\":{\"topic\":\"k8s\"},\"_meta\":{\"timeZone\":\"Not/AZone\"}}";
        var request = new ObjectMapper().readValue(body, ToolController.ToolCallRequest.class);
        assertEquals(Map.of("timeZone", "Not/AZone"), request.meta());
    }

    @Test
    void restBodyWithoutMetaStillParses() throws Exception {
        var request = new ObjectMapper().readValue("{\"arguments\":{}}", ToolController.ToolCallRequest.class);
        assertNull(request.meta());
    }
}
