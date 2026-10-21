package ai.kubemoot.agent.modelserver;

import java.util.Map;

/**
 * Ollama through its OpenAI-compatible API. Ollama turns thinking off only by
 * {@code reasoning_effort: none} on that API (its native {@code think} field is
 * ignored there); thinking models think by default, so {@code true} sends nothing.
 */
final class OllamaChatEngine implements ChatEngine {

    static final String TYPE = "ollama";

    @Override
    public String type() {
        return TYPE;
    }

    @Override
    public String chatBaseUrl(String endpoint) {
        return ChatEngine.withV1(endpoint);
    }

    @Override
    public Map<String, Object> thinkingFields(Boolean think) {
        return Boolean.FALSE.equals(think) ? Map.of("reasoning_effort", "none") : Map.of();
    }
}
