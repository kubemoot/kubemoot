package ai.kubemoot.agent.mcp;

import io.modelcontextprotocol.spec.McpSchema;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Unit tests for mapping an MCP CallToolResult (direct mode) to the agent's ToolResult,
 * read from the wire with the JSON binding direct mode hands the MCP SDK.
 */
class McpToolResultTest {

    @Test
    void textContentWithoutErrorIsSuccess() {
        var result = McpSchema.CallToolResult.builder()
                .addTextContent("3 pods running")
                .isError(false)
                .build();

        var mapped = McpClientService.toToolResult("k8s.pods_list", result);

        assertTrue(mapped.success());
        assertEquals("3 pods running", mapped.result());
        assertNull(mapped.error());
        assertEquals("k8s.pods_list", mapped.toolName());
    }

    @Test
    void absentIsErrorMeansSuccess() {
        var result = McpSchema.CallToolResult.builder()
                .addTextContent("ok")
                .build();

        var mapped = McpClientService.toToolResult("tool", result);

        assertTrue(mapped.success());
        assertEquals("ok", mapped.result());
    }

    @Test
    void errorResultCarriesTheTextAsError() {
        var result = McpSchema.CallToolResult.builder()
                .addTextContent("namespace not found")
                .isError(true)
                .build();

        var mapped = McpClientService.toToolResult("tool", result);

        assertFalse(mapped.success());
        assertEquals("namespace not found", mapped.error());
        assertEquals("namespace not found", mapped.result());
    }

    @Test
    void emptyContentGivesNoText() {
        var result = McpSchema.CallToolResult.builder()
                .content(List.of())
                .build();

        var mapped = McpClientService.toToolResult("tool", result);

        assertTrue(mapped.success());
        assertNull(mapped.result());
    }

    @Test
    void nonTextFirstContentGivesNoText() {
        var image = McpSchema.ImageContent.builder("aGVsbG8=", "image/png").build();
        var result = McpSchema.CallToolResult.builder()
                .addContent(image)
                .addTextContent("caption")
                .build();

        var mapped = McpClientService.toToolResult("tool", result);

        assertTrue(mapped.success());
        assertNull(mapped.result());
    }

    @Test
    void wireResultWithoutIsErrorDeserializesAsSuccess() throws Exception {
        var json = "{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}";

        var result = McpClientService.MCP_JSON.readValue(json, McpSchema.CallToolResult.class);
        var mapped = McpClientService.toToolResult("tool", result);

        assertTrue(mapped.success());
        assertEquals("done", mapped.result());
    }

    @Test
    void wireErrorResultDeserializesAsFailure() throws Exception {
        var json = "{\"content\":[{\"type\":\"text\",\"text\":\"boom\"}],\"isError\":true,\"extra\":1}";

        var result = McpClientService.MCP_JSON.readValue(json, McpSchema.CallToolResult.class);
        var mapped = McpClientService.toToolResult("tool", result);

        assertFalse(mapped.success());
        assertEquals("boom", mapped.error());
    }
}
