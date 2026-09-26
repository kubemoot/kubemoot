package ai.kubemoot.agent.chat;

import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;

/**
 * Reports agent readiness based on whether the Ollama server is reachable -
 * NOT on a pre-loaded model.
 *
 * WHY NO WARM-UP: this service used to send a throwaway chat("ready") inference
 * on startup to pre-load the model into VRAM. That was removed 2026-06-11. Pre-warming
 * (a) pins a model to the agent's static endpoint and BYPASSES the JIT scheduler,
 * which must be the only thing that places a model on a GPU; (b) makes every agent
 * call a GPU the instant it starts, so a deploy (which restarts all agents at once)
 * floods the GPUs with simultaneous model loads. See [[feedback_never_manual_warm]]
 * (cold start must equal warm start - the model loads on the first real query,
 * placed by the JIT scheduler, which already handles cold loads).
 *
 * READINESS now means: the process is up AND its Ollama server answers a cheap
 * GET /api/tags (a reachability check that lists installed models - it does NOT
 * load any model). isModelReady() flips true once that succeeds. The operator's
 * readiness probe and the coordinator wake-up signal consume isModelReady()
 * unchanged; they now get a fast, inference-free ready.
 */
@ApplicationScoped
public class ModelWarmupService {

    private static final Logger log = LoggerFactory.getLogger(ModelWarmupService.class);
    private static final int MAX_ATTEMPTS = 30;
    private static final int RETRY_DELAY_SECONDS = 5;

    private final String ollamaBaseUrl;
    private final HttpClient httpClient;
    private volatile boolean modelReady = false;

    public ModelWarmupService(@ConfigProperty(name = "quarkus.langchain4j.ollama.base-url",
                                      defaultValue = "http://localhost:11434") String ollamaUrl) {
        this.ollamaBaseUrl = ollamaUrl;
        this.httpClient = HttpClient.newBuilder()
                .connectTimeout(Duration.ofSeconds(3))
                .build();
    }

    void onStart(@Observes StartupEvent event) {
        // Readiness reflects process-up + Ollama reachable, never a pre-loaded
        // model. NO warm-up inference is sent: the model loads on the first real
        // query, placed by the JIT scheduler.
        if (isOllamaReachable()) {
            modelReady = true;
            log.info("Ollama reachable - agent ready (model loads on first query via JIT scheduler)");
            return;
        }
        // Ollama not up yet: poll reachability (a cheap GET, NOT an inference)
        // until it responds, then mark ready. Bounded so we never stay unready
        // forever (a truly dead Ollama is caught by the liveness probe).
        log.info("Ollama not yet reachable - polling reachability (no model pre-load)");
        Thread.ofVirtual().name("ollama-reachability").start(this::awaitReachable);
    }

    private void awaitReachable() {
        for (int attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
            if (isOllamaReachable()) {
                modelReady = true;
                log.info("Ollama reachable after {} attempt(s) - agent ready", attempt);
                return;
            }
            try {
                Thread.sleep(RETRY_DELAY_SECONDS * 1000L);
            } catch (InterruptedException ie) {
                Thread.currentThread().interrupt();
                break;
            }
        }
        log.warn("Ollama not reachable after {} attempts - marking ready to avoid permanent unready", MAX_ATTEMPTS);
        modelReady = true;
    }

    public boolean isModelReady() {
        return modelReady;
    }

    /**
     * Lightweight reachability check: GET /api/tags with a 3s timeout. Returns
     * true if Ollama responds 200. This lists installed models; it does NOT load
     * a model into VRAM.
     */
    public boolean isOllamaReachable() {
        try {
            var request = HttpRequest.newBuilder()
                    .uri(URI.create(ollamaBaseUrl + "/api/tags"))
                    .timeout(Duration.ofSeconds(3))
                    .GET()
                    .build();
            var response = httpClient.send(request, HttpResponse.BodyHandlers.discarding());
            return response.statusCode() == 200;
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return false;
        } catch (Exception e) {
            return false;
        }
    }
}
