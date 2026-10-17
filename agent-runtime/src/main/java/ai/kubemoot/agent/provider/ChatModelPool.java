package ai.kubemoot.agent.provider;

import dev.langchain4j.http.client.jdk.JdkHttpClientBuilder;
import dev.langchain4j.model.chat.ChatModel;
import dev.langchain4j.model.chat.StreamingChatModel;
import dev.langchain4j.model.openai.OpenAiChatModel;
import dev.langchain4j.model.openai.OpenAiStreamingChatModel;
import jakarta.enterprise.context.ApplicationScoped;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ConcurrentMap;

/**
 * Per-endpoint cache of LangChain4j chat models. The scheduler picks a provider
 * for each call, so a call may go to any server; this pool builds a model for a
 * server the first time a call goes there and reuses it (and its HTTP
 * connections) after that.
 *
 * Every model speaks the OpenAI-compatible chat API, whatever the engine behind
 * it; the provider's {@link ChatEngine} says where that API lives on the server
 * and how a request sets thinking.
 */
@ApplicationScoped
public class ChatModelPool {

    private static final Logger log = LoggerFactory.getLogger(ChatModelPool.class);

    /** The key sent to servers that need none (Ollama, an in-cluster vLLM). */
    static final String NO_API_KEY = "none";

    private final ConcurrentMap<String, ChatModel> cache = new ConcurrentHashMap<>();
    private final ConcurrentMap<String, StreamingChatModel> streamingCache = new ConcurrentHashMap<>();

    /**
     * Get or build a ChatModel for {@code engine}'s OpenAI-compatible API on the
     * server at {@code endpoint}. Idempotent and thread-safe.
     */
    public ChatModel forEndpoint(ChatEngine engine, String endpoint, String modelName, double temperature,
                                 int maxTokens, Duration timeout, Boolean think) {
        return cache.computeIfAbsent(key(engine, endpoint, modelName, temperature, maxTokens, timeout, think), k -> {
            log.info("Building ChatModel for engine={} endpoint={} model={} think={} (first use)",
                    engine.type(), endpoint, modelName, think);
            return OpenAiChatModel.builder()
                    // The JDK HTTP client, set explicitly: the native image has no
                    // HTTP client for LangChain4j to discover through ServiceLoader.
                    .httpClientBuilder(new JdkHttpClientBuilder())
                    .baseUrl(engine.chatBaseUrl(endpoint))
                    // Local servers take no key; the client still sends one.
                    .apiKey(NO_API_KEY)
                    .modelName(modelName)
                    .temperature(temperature)
                    .maxTokens(maxTokens)
                    .timeout(timeout)
                    // One attempt: LangChain4j retries a failed call by default, which
                    // multiplies a slow call's timeout. A failure surfaces once, as the
                    // agent's failure signal.
                    .maxRetries(1)
                    // Per-agent thinking control, in the engine's own request fields.
                    .customParameters(engine.thinkingFields(think))
                    .build();
        });
    }

    /**
     * Get or build the streaming twin of {@link #forEndpoint}: the same server, model,
     * and settings, for calls timed by their first token ({@link StreamedChat}).
     */
    public StreamingChatModel streamingForEndpoint(ChatEngine engine, String endpoint, String modelName,
                                                   double temperature, int maxTokens, Duration timeout,
                                                   Boolean think) {
        String key = key(engine, endpoint, modelName, temperature, maxTokens, timeout, think);
        return streamingCache.computeIfAbsent(key, k -> OpenAiStreamingChatModel.builder()
                .httpClientBuilder(new JdkHttpClientBuilder())
                .baseUrl(engine.chatBaseUrl(endpoint))
                .apiKey(NO_API_KEY)
                .modelName(modelName)
                .temperature(temperature)
                .maxTokens(maxTokens)
                .timeout(timeout)
                .customParameters(engine.thinkingFields(think))
                .build());
    }

    /** Every setting that changes the request; {@code think} is null when the agent leaves the default. */
    private static String key(ChatEngine engine, String endpoint, String modelName, double temperature,
                              int maxTokens, Duration timeout, Boolean think) {
        return engine.type() + "|" + endpoint + "|" + modelName + "|" + temperature + "|" + maxTokens
                + "|" + timeout.toMillis() + "|" + think;
    }

    /** Test-only; not part of normal use. */
    void clearForTest() {
        cache.clear();
        streamingCache.clear();
    }

    int sizeForTest() {
        return cache.size();
    }
}
