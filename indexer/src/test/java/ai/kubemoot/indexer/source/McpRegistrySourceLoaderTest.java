package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import ai.kubemoot.indexer.testsupport.StubHttpServer;
import ai.kubemoot.indexer.testsupport.StubHttpServer.Reply;
import org.junit.jupiter.api.Test;
import org.springframework.ai.document.Document;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

class McpRegistrySourceLoaderTest {

    private static final String CATALOG = """
        [
          {"name": "files", "description": "File access", "transport": "http", "image": "reg/files:1",
           "port": 8080, "categories": ["storage"],
           "tools": [
             {"name": "read", "description": "Read a file", "inputSchema": {"type": "object"}},
             {"name": "write", "description": "Write a file"}
           ]},
          {"name": "search", "description": "Web search", "categories": ["web"]}
        ]
        """;

    private McpRegistrySourceLoader loader(String url, List<String> filter) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.mcpRegistryUrl()).thenReturn(url);
        when(config.mcpRegistryFilter()).thenReturn(filter);
        return new McpRegistrySourceLoader(config, WebClient.builder());
    }

    @Test
    void supportsOnlyTheMcpRegistryType() {
        McpRegistrySourceLoader loader = loader("x", null);
        assertTrue(loader.supports("mcp-registry"));
        assertTrue(loader.supports("MCP-Registry"));
        assertFalse(loader.supports("url"));
    }

    @Test
    void everyToolBecomesADocumentAndAServerWithoutToolsBecomesOne() {
        try (StubHttpServer server = new StubHttpServer().route("GET", "/servers", Reply.ok(CATALOG))) {
            List<Document> documents = loader(server.baseUrl(), null).load();

            assertEquals(3, documents.size());
            Document read = documents.get(0);
            assertTrue(read.getText().contains("Tool: read"));
            assertEquals("files:read", read.getMetadata().get("full_tool_name"));
            assertEquals("http", read.getMetadata().get("transport"));
            assertEquals("reg/files:1", read.getMetadata().get("image"));
            assertEquals(8080, read.getMetadata().get("port"));
            assertEquals("{\"type\":\"object\"}", read.getMetadata().get("parameters"));
            assertFalse(documents.get(1).getMetadata().containsKey("parameters"));

            Document whole = documents.get(2);
            assertEquals("search", whole.getMetadata().get("full_tool_name"));
            assertEquals("_server", whole.getMetadata().get("tool_name"));
            assertFalse(whole.getMetadata().containsKey("transport"));
        }
    }

    @Test
    void theCategoryFilterKeepsOnlyMatchingServers() {
        try (StubHttpServer server = new StubHttpServer().route("GET", "/servers", Reply.ok(CATALOG))) {
            List<Document> documents = loader(server.baseUrl(), List.of("web")).load();

            assertEquals(1, documents.size());
            assertEquals("search", documents.get(0).getMetadata().get("server_name"));
        }
    }

    @Test
    void aFilterMatchingNoCategoryKeepsNothing() {
        try (StubHttpServer server = new StubHttpServer().route("GET", "/servers", Reply.ok(CATALOG))) {
            assertTrue(loader(server.baseUrl(), List.of("other")).load().isEmpty());
        }
    }

    @Test
    void loadRequiresARegistryUrl() {
        assertThrows(IllegalArgumentException.class, () -> loader(null, null).load());
        assertThrows(IllegalArgumentException.class, () -> loader(" ", null).load());
    }

    @Test
    void loadFailsOnAnUnreadableCatalog() {
        try (StubHttpServer server = new StubHttpServer().route("GET", "/servers", Reply.ok("{not json"))) {
            McpRegistrySourceLoader loader = loader(server.baseUrl(), null);
            assertThrows(IllegalStateException.class, loader::load);
        }
    }

    @Test
    void loadFailsWhenTheRegistryAnswersAnError() {
        try (StubHttpServer server = new StubHttpServer()) {
            McpRegistrySourceLoader loader = loader(server.baseUrl(), null);
            assertThrows(IllegalStateException.class, loader::load);
        }
    }
}
