package kubemoot.ai.mcpgateway.model;

import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.controller.ToolController;
import org.junit.jupiter.api.Test;

import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import java.util.LinkedHashMap;

/** The caller's {@code _meta} reaches the upstream tools/call unchanged, and is absent when the caller sent none. */
class McpMessageToolsCallTest {

    private static final Map<String, Object> ARGS = Map.of("topic", "kubernetes");

    @Test
    void validRequesterKeysAreForwarded() {
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

    private static Map<String, Object> forwarded(Map<String, Object> meta) {
        @SuppressWarnings("unchecked")
        var sent = (Map<String, Object>) McpMessage.toolsCall(1, "t", ARGS, meta).params().get("_meta");
        return sent;
    }

    @Test
    void forgedExtraKeysAreDropped() {
        var meta = new LinkedHashMap<String, Object>();
        meta.put("timeZone", "Europe/Paris");
        meta.put("authorization", "Bearer x");
        meta.put("progressToken", "p");
        assertEquals(Map.of("timeZone", "Europe/Paris"), forwarded(meta));
    }

    @Test
    void onlyForgedKeysMeansNoMetaKey() {
        assertFalse(McpMessage.toolsCall(1, "t", ARGS, Map.of("authorization", "x")).params().containsKey("_meta"));
    }

    @Test
    void invalidZoneIsDropped() {
        assertEquals(Map.of("requestDate", "2026-10-24"),
                forwarded(Map.of("timeZone", "Mars/Base", "requestDate", "2026-10-24")));
    }

    @Test
    void fiveDigitYearIsRejected() {
        assertNull(McpMessage.toolsCall(1, "t", ARGS,
                Map.of("localDateTime", "+12345-01-01T00:00:00", "requestDate", "12345-01-01")).params().get("_meta"));
    }

    @Test
    void overLongValuesAreRejected() {
        var longZone = "America/" + "x".repeat(100);
        var longLocal = "2026-10-24T09:00:00." + "1".repeat(40);
        assertNull(McpMessage.toolsCall(1, "t", ARGS,
                Map.of("timeZone", longZone, "localDateTime", longLocal)).params().get("_meta"));
    }

    @Test
    void nonMapOrNonStringValuesAreDropped() {
        assertNull(McpMessage.toolsCall(1, "t", ARGS, "not a map").params().get("_meta"));
        assertNull(McpMessage.toolsCall(1, "t", ARGS, Map.of("timeZone", 5)).params().get("_meta"));
    }
}
