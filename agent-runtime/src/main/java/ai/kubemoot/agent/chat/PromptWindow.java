package ai.kubemoot.agent.chat;

import java.util.OptionalLong;

/**
 * The context window of the model the scheduler picked for one call, known after the pick and
 * before the prompt is sent. The caller sizes its prompt to it, so a call that lands on a model with
 * a small window gets less text than one that lands on a large window.
 */
public interface PromptWindow {

    /** The window, in tokens, the picked model has; empty when no window is known for it. */
    OptionalLong tokens();

    /**
     * The room, in tokens, left in the window after {@code system} and {@code user} are sent and the
     * reply reserve is kept free; zero when they already fill it, empty when the window is unknown.
     */
    OptionalLong roomAfter(String system, String user);
}
