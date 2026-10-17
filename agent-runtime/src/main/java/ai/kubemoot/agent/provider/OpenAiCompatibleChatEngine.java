package ai.kubemoot.agent.provider;

import java.util.Map;

/**
 * Any other server that speaks the OpenAI-compatible API (vLLM, SGLang, llama.cpp's
 * server, a hosted endpoint). Thinking is turned off through the chat template, the
 * way vLLM and SGLang serve Qwen3 and similar models:
 * {@code chat_template_kwargs: {enable_thinking: false}}. Only turning it off sends
 * the field: thinking models think by default, and a strict hosted endpoint rejects
 * fields it does not know.
 */
final class OpenAiCompatibleChatEngine implements ChatEngine {

    /** This engine's label in the model cache key; {@link ChatEngines#forType} maps every type but ollama here. */
    static final String TYPE = "openai";

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
        return Boolean.FALSE.equals(think)
                ? Map.of("chat_template_kwargs", Map.of("enable_thinking", false))
                : Map.of();
    }
}
