package ai.kubemoot.agent.provider;

import java.util.Map;

/**
 * How an agent sends a chat call to one kind of model server. Every engine is
 * called through the OpenAI-compatible chat API ({@code /v1/chat/completions});
 * an engine supplies only what that API leaves to it: where the API lives on
 * the server, and the request fields that turn the model's thinking on or off.
 * {@link ChatEngines} picks one by the provider's engine type.
 */
public interface ChatEngine {

    /** The engine type a ModelProvider declares, for example {@code ollama}. */
    String type();

    /** The base URL of the OpenAI-compatible API on a server at {@code endpoint}. */
    String chatBaseUrl(String endpoint);

    /**
     * The request fields beyond the common API that set thinking: {@code false}
     * turns it off, {@code true} on; {@code null} sends nothing and leaves the
     * model's default.
     */
    Map<String, Object> thinkingFields(Boolean think);

    /** {@code endpoint} with {@code /v1} appended unless it already ends with it. */
    static String withV1(String endpoint) {
        String base = endpoint == null ? "" : endpoint.replaceAll("/+$", "");
        return base.endsWith("/v1") ? base : base + "/v1";
    }
}
