package ai.kubemoot.agent.provider;

import java.util.Locale;

/** The chat engine for a provider's engine type. */
public final class ChatEngines {

    /** Ollama, today's engine and the default when a provider names none. */
    public static final ChatEngine OLLAMA = new OllamaChatEngine();
    /** Any other OpenAI-compatible server. */
    public static final ChatEngine OPENAI_COMPATIBLE = new OpenAiCompatibleChatEngine();

    private ChatEngines() {}

    /**
     * The engine for {@code type}: Ollama when it is absent or {@code ollama} (providers
     * published before the type was), else the OpenAI-compatible engine, which needs
     * nothing engine-specific beyond the API.
     */
    public static ChatEngine forType(String type) {
        return isOllama(type) ? OLLAMA : OPENAI_COMPATIBLE;
    }

    /** True for {@code ollama} and for a type that is absent (providers published before the type was). */
    public static boolean isOllama(String type) {
        return type == null || type.isBlank() || OllamaChatEngine.TYPE.equals(type.trim().toLowerCase(Locale.ROOT));
    }
}
