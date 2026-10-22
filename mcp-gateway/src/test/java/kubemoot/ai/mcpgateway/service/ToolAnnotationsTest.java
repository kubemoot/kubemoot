package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import kubemoot.ai.mcpgateway.model.ToolInfo;
import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ToolAnnotationsTest {

    @Test
    void passesOnTheAnnotationsAServerSends() {
        Map<String, Object> annotations = Map.of("readOnlyHint", true, "idempotentHint", true);
        Map<String, Object> entry = new HashMap<>(Map.of("name", "search", "annotations", annotations));

        assertEquals(annotations, McpClientManager.annotationsOf(entry));
    }

    @Test
    void anAbsentEmptyOrMalformedAnnotationsFieldIsNull() {
        assertNull(McpClientManager.annotationsOf(Map.of("name", "search")));
        assertNull(McpClientManager.annotationsOf(Map.of("annotations", Map.of())));
        assertNull(McpClientManager.annotationsOf(Map.of("annotations", "read-only")));
    }

    @Test
    void theToolListCarriesAnnotationsOnlyWhenPresent() throws Exception {
        ObjectMapper mapper = new ObjectMapper();
        ToolInfo plain = new ToolInfo("a", "d", Map.of(), "id", "srv");
        ToolInfo annotated = new ToolInfo("b", "d", Map.of(), "id", "srv", Map.of("readOnlyHint", true));

        assertFalse(mapper.writeValueAsString(plain).contains("annotations"));
        assertTrue(mapper.writeValueAsString(annotated).contains("\"annotations\":{\"readOnlyHint\":true}"));
    }
}
