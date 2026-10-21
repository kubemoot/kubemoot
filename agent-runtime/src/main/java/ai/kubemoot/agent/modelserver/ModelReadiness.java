package ai.kubemoot.agent.modelserver;

/**
 * Makes a model ready on the server a call was placed on, before the call is sent. Ollama
 * loads on demand when the call arrives, so nothing is asked of it; an engine that keeps
 * models asleep (vLLM) is woken by the operator, and the call waits for the server to
 * report the model awake.
 */
public final class ModelReadiness {

    private ModelReadiness() {}

    /**
     * Returns when {@code model} can take the call on {@code pick}'s server. Does nothing
     * when the server's state already shows it awake. When the server cannot make it
     * ready, the pick's ticket is released, the failure is recorded against the server so
     * placement avoids it, and the call fails with the reason.
     *
     * @throws IllegalStateException when the server could not make the model ready
     */
    public static void ensure(ServerSelector selector, CallPlacement placement, Pick pick, String model) {
        ServerState server = pick.server();
        if (selector == null || server.hasModelLoaded(model)) {
            return;
        }
        try {
            if (selector.driverFor(server).ensureReady(server, model)) {
                selector.invalidateCache();
            }
        } catch (ModelNotReadyException e) {
            placement.releaseTicket(pick.ticket());
            placement.recordFailure(server.name());
            throw new IllegalStateException(e.getMessage(), e);
        }
    }
}
