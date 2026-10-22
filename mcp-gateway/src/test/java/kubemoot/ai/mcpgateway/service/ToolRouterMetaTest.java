package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.McpMessage;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import reactor.core.publisher.Mono;

import java.util.LinkedHashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

/** The MCP endpoint forwards only the requester keys of a caller's {@code _meta}. */
class ToolRouterMetaTest {

    private final McpClientManager manager = mock(McpClientManager.class);
    private final ToolRouter router = new ToolRouter(manager, null);

    private McpMessage forwardedFor(Object meta) {
        when(manager.findServerForTool("t")).thenReturn("s1");
        when(manager.forwardRequest(any(), any())).thenReturn(Mono.just(McpMessage.response(1, Map.of())));
        var params = new LinkedHashMap<String, Object>();
        params.put("name", "t");
        params.put("arguments", Map.of());
        params.put("_meta", meta);
        router.handleRequest(McpMessage.request(1, "tools/call", params)).block();
        var captor = ArgumentCaptor.forClass(McpMessage.class);
        verify(manager).forwardRequest(eq("s1"), captor.capture());
        return captor.getValue();
    }

    @Test
    void forgedKeysAndInvalidValuesAreDropped() {
        var meta = new LinkedHashMap<String, Object>();
        meta.put("timeZone", "Europe/Paris");
        meta.put("localDateTime", "not a date");
        meta.put("secret", "x");
        assertEquals(Map.of("timeZone", "Europe/Paris"), forwardedFor(meta).params().get("_meta"));
    }

    @Test
    void aNonMapMetaIsDropped() {
        assertFalse(forwardedFor("oops").params().containsKey("_meta"));
    }
}
