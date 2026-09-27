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
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Client for querying RAG (Retrieval Augmented Generation) sources.
 */
@ApplicationScoped
public class RagClient {

    private static final Logger log = LoggerFactory.getLogger(RagClient.class);

    private final List<AgentProperties.RagSource> sources;
    private final HttpClient httpClient;
    private final ObjectMapper mapper;

    public RagClient(AgentProperties properties, ObjectMapper mapper) {
        this.sources = properties.ragSources().orElse(List.of());
        this.mapper = mapper;
        // HTTP/1.1, as ResumeSearchClient does: the default HTTP/2 client sends an h2c
        // upgrade the Python query service does not accept, and it answers 422
        // without the request body.
        this.httpClient = HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_1_1)
                .connectTimeout(Duration.ofSeconds(10))
                .build();
        log.info("Initialized RAG client with {} sources", sources.size());
    }

    public record RagResult(String source, String content, double score, Map<String, Object> metadata) {}

    public List<RagResult> query(String query) {
        var results = new ArrayList<RagResult>();
        for (var source : sources) {
            if (source.topK() <= 0) continue;
            results.addAll(querySource(source, query));
        }
        return results;
    }

    public String queryForContext(String query) {
        var results = query(query);
        return formatContext(results);
    }

    private List<RagResult> querySource(AgentProperties.RagSource source, String query) {
        log.debug("Querying RAG source {} with topK={}", source.name(), source.topK());
        try {
            var body = mapper.writeValueAsString(Map.of("query", query, "top_k", source.topK()));
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(source.endpoint() + "/query"))
                    .timeout(Duration.ofSeconds(30))
                    .header("Content-Type", "application/json")
                    .POST(HttpRequest.BodyPublishers.ofString(body))
                    .build();

            var response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());
            if (response.statusCode() < 200 || response.statusCode() >= 300) {
                log.warn("RAG source {} answered HTTP {}: {}", source.name(), response.statusCode(),
                        response.body().length() > 200 ? response.body().substring(0, 200) : response.body());
                return List.of();
            }
            var queryResponse = mapper.readValue(response.body(), QueryResponse.class);

            if (queryResponse.results() == null) return List.of();

            return queryResponse.results().stream()
                    .map(result -> new RagResult(source.name(), result.content(), result.score(),
                            result.metadata() != null ? result.metadata() : Map.of()))
                    .toList();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            log.warn("Failed to query RAG source {}: {}", source.name(), e.getMessage());
            return List.of();
        } catch (Exception e) {
            log.warn("Failed to query RAG source {}: {}", source.name(), e.getMessage());
            return List.of();
        }
    }

    private String formatContext(List<RagResult> results) {
        if (results.isEmpty()) return "";
        var sb = new StringBuilder("## Retrieved Context\n\n");
        String currentSource = null;
        for (var result : results) {
            if (!result.source().equals(currentSource)) {
                currentSource = result.source();
                sb.append("### From: ").append(currentSource).append("\n\n");
            }
            sb.append(result.content()).append("\n\n");
        }
        return sb.toString();
    }

    public Map<String, Boolean> healthCheck() {
        var health = new HashMap<String, Boolean>();
        for (var source : sources) {
            try {
                var request = HttpRequest.newBuilder()
                        .uri(URI.create(source.endpoint() + "/health"))
                        .timeout(Duration.ofSeconds(5))
                        .GET()
                        .build();
                var response = httpClient.send(request, HttpResponse.BodyHandlers.discarding());
                health.put(source.name(), response.statusCode() >= 200 && response.statusCode() < 300);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                health.put(source.name(), false);
            } catch (Exception e) {
                health.put(source.name(), false);
            }
        }
        return health;
    }

    private record QueryResponse(List<QueryResult> results) {}
    private record QueryResult(String content, double score, Map<String, Object> metadata) {}
}
