package ai.kubemoot.agent.rag;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import jakarta.enterprise.context.ApplicationScoped;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/**
 * Client for searching agent resumes via the resume query service (pgvector-backed).
 * Used by the coordinator's triage phase to pre-filter agents by semantic relevance
 * before sending the full list to the LLM.
 *
 * Graceful degradation: returns null on any error, allowing the caller to fall back
 * to the existing ConfigMap-based resume loading path.
 *
 * Uses java.net.http.HttpClient (GraalVM native compatible — no new library dependencies).
 */
@ApplicationScoped
public class ResumeSearchClient {

    private static final Logger log = LoggerFactory.getLogger(ResumeSearchClient.class);

    // HTTP timeouts for the resume query service.
    private static final Duration CONNECT_TIMEOUT = Duration.ofSeconds(5);
    private static final Duration QUERY_TIMEOUT = Duration.ofSeconds(10);
    private static final Duration HEALTH_TIMEOUT = Duration.ofSeconds(3);
    // Max chars of the query echoed into logs.
    private static final int LOG_QUERY_CHARS = 80;
    // Max chars of an error response body echoed into a warning log.
    private static final int LOG_ERROR_BODY_CHARS = 200;

    private final String endpoint;
    private final HttpClient httpClient;
    private final ObjectMapper mapper;

    public ResumeSearchClient(AgentProperties properties, ObjectMapper mapper) {
        this.endpoint = properties.resumeSearch().endpoint().orElse(null);
        this.mapper = mapper;
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(CONNECT_TIMEOUT)
                .version(HttpClient.Version.HTTP_1_1)
                .build();
        if (endpoint != null) {
            log.info("Resume search client configured with endpoint: {}", endpoint);
        } else {
            log.debug("Resume search endpoint not configured, semantic triage pre-filtering disabled");
        }
    }

    /**
     * Search for agent resumes semantically similar to the given query.
     * Returns agent names extracted from result metadata (metadata.agent_name),
     * NOT from the content text — metadata is structured and reliable.
     *
     * @param query  the user's question to match against resume embeddings
     * @param topK   maximum number of results to return
     * @return list of agent names, or null if search unavailable/failed
     */
    public List<String> searchResumes(String query, int topK) {
        if (endpoint == null || endpoint.isEmpty()) {
            return null;
        }

        try {
            // Build JSON manually to avoid GraalVM native serialization issues with Map<String, Object>
            var escapedQuery = query.replace("\\", "\\\\").replace("\"", "\\\"");
            var body = String.format("{\"query\":\"%s\",\"top_k\":%d}", escapedQuery, topK);
            var bodyBytes = body.getBytes(java.nio.charset.StandardCharsets.UTF_8);
            log.info("Resume search: POST {}/query (query={}, bodyLen={}, body={})", endpoint, truncate(query, LOG_QUERY_CHARS), bodyBytes.length, body);
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(endpoint + "/query"))
                    .timeout(QUERY_TIMEOUT)
                    .header("Content-Type", "application/json")
                    .expectContinue(false)
                    .POST(HttpRequest.BodyPublishers.ofByteArray(bodyBytes))
                    .build();

            var response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());

            if (response.statusCode() != 200) {
                log.warn("Resume search returned status {}: {}", response.statusCode(),
                        truncate(response.body(), LOG_ERROR_BODY_CHARS));
                return null;
            }

            // IMPORTANT: Do NOT use record classes or mapper.readValue(body, SomeClass.class) here.
            // GraalVM native cannot deserialize Java records or classes without explicit reflection
            // configuration. Use readTree() for manual JSON navigation — it requires no reflection.
            // This was the root cause of a multi-hour debugging session (2026-03-23).
            var responseNode = mapper.readTree(response.body());
            var resultsNode = responseNode.get("results");
            if (resultsNode == null || !resultsNode.isArray() || resultsNode.isEmpty()) {
                log.debug("Resume search returned no results for query: {}", truncate(query, LOG_QUERY_CHARS));
                return null;
            }

            var agentNames = agentNamesFrom(resultsNode);

            log.info("Resume search returned {} agents for triage pre-filtering: {}", agentNames.size(), agentNames);
            return agentNames;

        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.warn("Resume search failed (falling back to ConfigMap): {}", e.getMessage(), e);
            return null;
        } catch (Exception e) {
            log.warn("Resume search failed (falling back to ConfigMap): {}", e.getMessage(), e);
            return null;
        }
    }

    /**
     * Agent names from the search results' metadata, de-duplicated in result order.
     * Skill docs have kind=skill and skill_name but NO agent_name, so they are skipped
     * and a skill doc never becomes a bogus agent name in the selection.
     */
    static List<String> agentNamesFrom(com.fasterxml.jackson.databind.JsonNode resultsNode) {
        var seen = new java.util.LinkedHashSet<String>();
        for (var resultNode : resultsNode) {
            var metadataNode = resultNode.get("metadata");
            if (isAgentMetadata(metadataNode)) {
                seen.add(metadataNode.get("agent_name").asText());
            }
        }
        return new ArrayList<>(seen);
    }

    /**
     * True when the metadata node represents an agent doc (has agent_name, not a skill doc).
     * Skill docs have kind=skill and skill_name but no agent_name.
     */
    static boolean isAgentMetadata(com.fasterxml.jackson.databind.JsonNode metadata) {
        return metadata != null && metadata.has("agent_name");
    }

    /**
     * Check if the resume search endpoint is configured and reachable.
     */
    public boolean isAvailable() {
        if (endpoint == null || endpoint.isEmpty()) {
            return false;
        }
        try {
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(endpoint + "/health"))
                    .timeout(HEALTH_TIMEOUT)
                    .GET()
                    .build();
            var response = httpClient.send(request, HttpResponse.BodyHandlers.discarding());
            return response.statusCode() >= 200 && response.statusCode() < 300;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (Exception e) {
            return false;
        }
    }

    private static String truncate(String s, int maxLen) {
        if (s == null) return "";
        return s.length() > maxLen ? s.substring(0, maxLen) + "..." : s;
    }

}
