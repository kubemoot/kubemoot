package ai.kubemoot.agent.chat;

/**
 * The model one inference call ran on and the ModelServer that served it, as the
 * scheduler placed the call. The server is empty when no server was picked and
 * the call went to the agent's static endpoint; a null model or server is
 * stored as empty.
 *
 * @param model  the model the call ran on
 * @param server the ModelServer name, or empty for the static endpoint
 * @param call   the placement behind the call and what it measured; null when the
 *               call was not placed by the scheduler
 */
public record ModelUse(String model, String server, ai.kubemoot.agent.modelserver.PlacedCall call) {

    public ModelUse {
        model = model == null ? "" : model;
        server = server == null ? "" : server;
    }

    /** A use whose placement is not followed. */
    public ModelUse(String model, String server) {
        this(model, server, null);
    }

    /** {@code model@server}, or the model alone when no server was picked. */
    public String label() {
        return server.isEmpty() ? model : model + "@" + server;
    }
}
