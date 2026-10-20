package ai.kubemoot.agent.chat;

/**
 * The model one inference call ran on and the ModelServer that served it, as the
 * scheduler placed the call. The server is empty when no server was picked and
 * the call went to the agent's static endpoint; a null model or server is
 * stored as empty.
 *
 * @param model    the model the call ran on
 * @param server the ModelServer name, or empty for the static endpoint
 */
public record ModelUse(String model, String server) {

    public ModelUse {
        model = model == null ? "" : model;
        server = server == null ? "" : server;
    }

    /** {@code model@server}, or the model alone when no server was picked. */
    public String label() {
        return server.isEmpty() ? model : model + "@" + server;
    }
}
