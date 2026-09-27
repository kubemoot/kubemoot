package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;

/**
 * Loads and unloads models on an Ollama provider through {@code POST /api/generate}
 * with no prompt: {@code keep_alive: 0} unloads the model, and a request without
 * {@code keep_alive} loads it with the provider's default keep-alive. This is the
 * only place the platform unloads a model.
 */
@ApplicationScoped
public class OllamaModelControl {

    private static final Logger log = LoggerFactory.getLogger(OllamaModelControl.class);
    private static final Duration UNLOAD_TIMEOUT = Duration.ofSeconds(30);
    private static final Duration LOAD_TIMEOUT = Duration.ofMinutes(5);

    private final ObjectMapper mapper;
    private final HttpClient http;

    @Inject
    public OllamaModelControl(ObjectMapper mapper) {
        this.mapper = mapper;
        this.http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(10)).build();
    }

    /** Unloads {@code model} from the provider at {@code endpoint}. Returns true on a 2xx answer. */
    public boolean unload(String endpoint, String model) {
        try {
            HttpResponse<Void> resp = http.send(request(endpoint, model, true, UNLOAD_TIMEOUT),
                    HttpResponse.BodyHandlers.discarding());
            boolean ok = resp.statusCode() / 100 == 2;
            log.info("Unloaded model {} on {} (status {})", model, endpoint, resp.statusCode());
            return ok;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (Exception e) {
            log.warn("Unloading model {} on {} failed: {}", model, endpoint, e.getMessage());
            return false;
        }
    }

    /** Starts loading {@code model} on the provider at {@code endpoint} without waiting for it. */
    public void startLoad(String endpoint, String model) {
        try {
            http.sendAsync(request(endpoint, model, false, LOAD_TIMEOUT), HttpResponse.BodyHandlers.discarding())
                    .whenComplete((r, e) -> log.info("Load of {} on {} finished ({})", model, endpoint,
                            e == null ? "status " + r.statusCode() : e.getMessage()));
            log.info("Started loading model {} on {} for a selected agent", model, endpoint);
        } catch (Exception e) {
            log.warn("Starting load of {} on {} failed: {}", model, endpoint, e.getMessage());
        }
    }

    // Visible for testing
    String body(String model, boolean unload) throws Exception {
        ObjectNode n = mapper.createObjectNode();
        n.put("model", model);
        if (unload) {
            n.put("keep_alive", 0);
        }
        return mapper.writeValueAsString(n);
    }

    private HttpRequest request(String endpoint, String model, boolean unload, Duration timeout) throws Exception {
        return HttpRequest.newBuilder(URI.create(endpoint + "/api/generate"))
                .timeout(timeout)
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(body(model, unload)))
                .build();
    }
}
