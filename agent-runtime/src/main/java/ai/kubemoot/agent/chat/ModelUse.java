package ai.kubemoot.agent.chat;

/**
 * The model one inference call ran on and the ModelProvider that served it, as the
 * scheduler placed the call. The provider is empty when no provider was picked and
 * the call went to the agent's static endpoint; a null model or provider is
 * stored as empty.
 *
 * @param model    the model the call ran on
 * @param provider the ModelProvider name, or empty for the static endpoint
 */
public record ModelUse(String model, String provider) {

    public ModelUse {
        model = model == null ? "" : model;
        provider = provider == null ? "" : provider;
    }

    /** {@code model@provider}, or the model alone when no provider was picked. */
    public String label() {
        return provider.isEmpty() ? model : model + "@" + provider;
    }
}
