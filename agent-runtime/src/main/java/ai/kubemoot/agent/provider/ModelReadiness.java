package ai.kubemoot.agent.provider;

/**
 * Makes a model ready on the provider a call was placed on, before the call is sent. Ollama
 * loads on demand when the call arrives, so nothing is asked of it; an engine that keeps
 * models asleep (vLLM) is woken by the operator, and the call waits for the provider to
 * report the model awake.
 */
public final class ModelReadiness {

    private ModelReadiness() {}

    /**
     * Returns when {@code model} can take the call on {@code pick}'s provider. Does nothing
     * when the provider's state already shows it awake. When the provider cannot make it
     * ready, the pick's ticket is released, the failure is recorded against the provider so
     * placement avoids it, and the call fails with the reason.
     *
     * @throws IllegalStateException when the provider could not make the model ready
     */
    public static void ensure(ProviderSelector selector, TicketManager tickets, Pick pick, String model) {
        ProviderState provider = pick.provider();
        if (selector == null || provider.hasModelLoaded(model)) {
            return;
        }
        try {
            if (selector.driverFor(provider).ensureReady(provider, model)) {
                selector.invalidateCache();
            }
        } catch (ModelNotReadyException e) {
            tickets.release(pick.ticket());
            selector.recordFailure(provider.name());
            throw new IllegalStateException(e.getMessage(), e);
        }
    }
}
