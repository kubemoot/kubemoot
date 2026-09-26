package ai.kubemoot.agent.provider;

import dev.langchain4j.http.client.jdk.JdkHttpClientBuilder;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.ollama.OllamaChatModel;
import jakarta.enterprise.context.ApplicationScoped;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ConcurrentMap;

/**
 * Per-endpoint cache of LangChain4j ChatModel instances. The Quarkus-injected
 * ChatModel is fixed to a single base-url at startup, which is the wrong shape
 * for JIT provider selection — each inference call may need a different
 * endpoint. This pool gives ChatService a way to get a properly-configured
 * ChatModel for any endpoint the {@link ProviderSelector} returns, building
 * each one lazily on first use and reusing it (and its underlying HTTP
 * connection pool) thereafter.
 *
 * Construction is cheap-ish (~10-50ms first call for an endpoint as the
 * underlying HttpClient initialises; subsequent calls reuse). All ChatModel
 * instances share the same model/temperature/timeout settings; only the
 * baseUrl differs. If those settings need to vary per provider in future,
 * extend the key beyond just endpoint.
 *
 * Thread-safe via ConcurrentHashMap.computeIfAbsent — no lock contention
 * once the pool is warm.
 */
@ApplicationScoped
public class ChatModelPool {

    private static final Logger log = LoggerFactory.getLogger(ChatModelPool.class);

    private final ConcurrentMap<String, ChatModel> cache = new ConcurrentHashMap<>();

    /**
     * Get or build a ChatModel bound to the given endpoint + model + timeout.
     * Idempotent and thread-safe.
     */
    public ChatModel forEndpoint(String endpoint, String modelName, double temperature,
                                  int maxTokens, Duration timeout, Boolean think) {
        // Cache key combines the parameters that determine the model's
        // identity. If two calls request the same endpoint + model with
        // different temperature, they'd otherwise collide; including
        // temperature in the key keeps them separate. Cheap to maintain.
        // `think` is included because it changes the ollama request shape
        // (thinking on/off); null means "leave the model default".
        String key = endpoint + "|" + modelName + "|" + temperature + "|" + maxTokens
                + "|" + timeout.toMillis() + "|" + think;
        return cache.computeIfAbsent(key, k -> {
            log.info("Building ChatModel for endpoint={} model={} think={} (first use)",
                    endpoint, modelName, think);
            var builder = OllamaChatModel.builder()
                    // Explicit JDK HTTP client. Without this the builder uses
                    // ServiceLoader to discover an HttpClientBuilder, which
                    // finds NONE in GraalVM native (the Quarkus extension
                    // excludes langchain4j-http-client-jdk and provides its
                    // client only to the injected model). Setting it directly
                    // bypasses ServiceLoader and uses java.net.http — the same
                    // native-safe client the triage path uses. Without it,
                    // every JIT-built model threw "No HTTP client has been
                    // found in the classpath" (observed 2026-05-26, thread
                    // 297dea21, right after JIT selection started engaging).
                    .httpClientBuilder(new JdkHttpClientBuilder())
                    .baseUrl(endpoint)
                    .modelName(modelName)
                    .temperature(temperature)
                    .numPredict(maxTokens)
                    .timeout(timeout)
                    // FIX #1 of three: bound LangChain4j's internal retry loop
                    // to a single attempt. The default is 3 attempts, which
                    // multiplied by the Quarkus/Vertx HTTP timeout (120s)
                    // turns a single slow call into a 6+ minute silent retry
                    // storm — observed concretely 2026-05-25 on k8s-metrics'
                    // mulling call to ollama-rig1: 3 × 120s with no visible
                    // signal except heartbeats, then a generic stand_aside.
                    // With maxRetries(1), a timeout surfaces as a single
                    // failed call in 120s; our failure-signal path in
                    // DiscussionSubscriber.runMullingPhase catches it and
                    // publishes the `failure` signal with cause metadata —
                    // the dashboard sees it, the coordinator can settle.
                    .maxRetries(1);
            // Thinking control. Per-agent declared behavior (KUBEMOOT_MODEL_THINK)
            // plumbed generically — the runtime does not special-case any agent
            // role here; it only honors the configured value. think=false stops
            // qwen3 from emitting a reasoning chain (deterministic tool-callers
            // do not need it and it dominates their latency); think=true keeps
            // it (analysts, coordinator). null leaves the model/family default.
            if (think != null) {
                builder.think(think);
            }
            return builder.build();
        });
    }

    /** Test-only; not part of normal use. */
    void clearForTest() {
        cache.clear();
    }

    int sizeForTest() {
        return cache.size();
    }
}
