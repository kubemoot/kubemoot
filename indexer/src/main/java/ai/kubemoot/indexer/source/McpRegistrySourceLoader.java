package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.stereotype.Component;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.*;

/**
 * Loads MCP tool descriptions from MCP registries (e.g., mcp.run).
 *
 * Each tool becomes a Document with:
 * - content: Tool name + description (for semantic search)
 * - metadata: server_name, tool_name, parameters, transport, etc.
 */
@Component
public class McpRegistrySourceLoader implements SourceLoader {

    private static final Logger logger = LoggerFactory.getLogger(McpRegistrySourceLoader.class);

    // Metadata key constants
    private static final String META_SOURCE = "source";
    private static final String META_SOURCE_VALUE = "mcp-registry";
    private static final String META_REGISTRY_URL = "registry_url";
    private static final String META_SERVER_NAME = "server_name";
    private static final String META_TOOL_NAME = "tool_name";
    private static final String META_FULL_TOOL_NAME = "full_tool_name";
    private static final String META_TRANSPORT = "transport";
    private static final String META_IMAGE = "image";
    private static final String META_PORT = "port";

    // JSON field names read from registry server/tool nodes
    private static final String FIELD_NAME = "name";
    private static final String FIELD_DESCRIPTION = "description";

    private final IndexerConfig config;
    private final WebClient webClient;
    private final ObjectMapper objectMapper;

    public McpRegistrySourceLoader(IndexerConfig config, WebClient.Builder webClientBuilder) {
        this.config = config;
        this.webClient = Downloads.client(webClientBuilder);
        this.objectMapper = new ObjectMapper();
    }

    @Override
    public boolean supports(String sourceType) {
        return META_SOURCE_VALUE.equalsIgnoreCase(sourceType);
    }

    @Override
    public List<Document> load() {
        if (config.mcpRegistryUrl() == null || config.mcpRegistryUrl().isBlank()) {
            throw new IllegalArgumentException("KUBEMOOT_MCP_REGISTRY_URL is required for mcp-registry source");
        }

        logger.info("Fetching MCP catalog from: {}", config.mcpRegistryUrl());

        try {
            // Fetch the catalog from the registry
            String response = webClient.get()
                .uri(config.mcpRegistryUrl() + "/servers")
                .retrieve()
                .bodyToMono(String.class)
                .block();

            JsonNode catalog = objectMapper.readTree(response);
            List<Document> documents = new ArrayList<>();

            // Process each server in the catalog
            for (JsonNode server : catalog) {
                addServerDocuments(server, documents);
            }

            logger.info("Loaded {} tool documents from MCP registry", documents.size());
            return documents;

        } catch (Exception e) {
            throw new IllegalStateException("Failed to load MCP registry", e);
        }
    }

    private void addServerDocuments(JsonNode server, List<Document> documents) {
        String serverName = server.path(FIELD_NAME).asText();
        String serverDescription = server.path(FIELD_DESCRIPTION).asText("");

        // Check category filter if specified
        if (!matchesCategoryFilter(server)) {
            return;
        }

        // Get tools for this server
        JsonNode tools = server.path("tools");
        if (tools.isArray()) {
            for (JsonNode tool : tools) {
                Document doc = createToolDocument(serverName, serverDescription, tool, server);
                documents.add(doc);
            }
        } else {
            // Server without explicit tools - create a document for the server itself
            Document doc = createServerDocument(serverName, serverDescription, server);
            documents.add(doc);
        }
    }

    private boolean matchesCategoryFilter(JsonNode server) {
        if (config.mcpRegistryFilter() == null || config.mcpRegistryFilter().isEmpty()) {
            return true;
        }
        JsonNode categories = server.path("categories");
        if (categories.isArray()) {
            for (JsonNode cat : categories) {
                if (config.mcpRegistryFilter().contains(cat.asText())) {
                    return true;
                }
            }
        }
        return false;
    }

    private Document createToolDocument(String serverName, String serverDescription,
                                         JsonNode tool, JsonNode server) {
        String toolName = tool.path(FIELD_NAME).asText();
        String toolDescription = tool.path(FIELD_DESCRIPTION).asText("");

        // Create searchable content combining server and tool info
        String content = String.format("""
            Server: %s
            Tool: %s
            Description: %s
            Server Description: %s
            """,
            serverName, toolName, toolDescription, serverDescription);

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(META_SOURCE, META_SOURCE_VALUE);
        metadata.put(META_REGISTRY_URL, config.mcpRegistryUrl());
        metadata.put(META_SERVER_NAME, serverName);
        metadata.put(META_TOOL_NAME, toolName);
        metadata.put(META_FULL_TOOL_NAME, serverName + ":" + toolName);

        // Include transport info if available
        if (server.has(META_TRANSPORT)) {
            metadata.put(META_TRANSPORT, server.path(META_TRANSPORT).asText());
        }

        // Include image info for provisioning
        if (server.has(META_IMAGE)) {
            metadata.put(META_IMAGE, server.path(META_IMAGE).asText());
        }
        if (server.has(META_PORT)) {
            metadata.put(META_PORT, server.path(META_PORT).asInt());
        }

        // Include tool parameters schema if available
        if (tool.has("inputSchema")) {
            metadata.put("parameters", tool.path("inputSchema").toString());
        }

        return new Document(content, metadata);
    }

    private Document createServerDocument(String serverName, String serverDescription,
                                           JsonNode server) {
        String content = String.format("""
            Server: %s
            Description: %s
            """,
            serverName, serverDescription);

        Map<String, Object> metadata = new HashMap<>();
        metadata.put(META_SOURCE, META_SOURCE_VALUE);
        metadata.put(META_REGISTRY_URL, config.mcpRegistryUrl());
        metadata.put(META_SERVER_NAME, serverName);
        metadata.put(META_TOOL_NAME, "_server");
        metadata.put(META_FULL_TOOL_NAME, serverName);

        if (server.has(META_TRANSPORT)) {
            metadata.put(META_TRANSPORT, server.path(META_TRANSPORT).asText());
        }
        if (server.has(META_IMAGE)) {
            metadata.put(META_IMAGE, server.path(META_IMAGE).asText());
        }
        if (server.has(META_PORT)) {
            metadata.put(META_PORT, server.path(META_PORT).asInt());
        }

        return new Document(content, metadata);
    }
}
