package ai.kubemoot.agent.chat;

/**
 * The system and user text of one no-tool call, as built once the call's window is known.
 * {@code maxOutputTokens} caps the reply (the model's configured limit when it is not above zero).
 */
public record SimplePrompt(String system, String user, int maxOutputTokens) {

    /** A prompt whose reply is limited only by the model's configured limit. */
    public SimplePrompt(String system, String user) {
        this(system, user, 0);
    }
}
