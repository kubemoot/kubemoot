package ai.kubemoot.agent.modelserver;

/**
 * Ollama: models load on demand into accelerator memory, so a model is served when it
 * is resident or fits (see {@link StaticFitPredictor#predictByMemory}), crowding is
 * the in-flight calls per parallel slot, and a ticket claims from the memory left
 * after the resident models.
 */
final class OllamaFit implements EngineFit {

    static final OllamaFit INSTANCE = new OllamaFit();

    private OllamaFit() {}

    @Override
    public FitScore predict(FitInputs in) {
        return StaticFitPredictor.predictByMemory(in);
    }

    @Override
    public boolean budgetsMemory() {
        return true;
    }

    @Override
    public double contention(ServerState server, int inFlightCalls) {
        return (double) inFlightCalls / Math.max(server.maxParallel(), 1);
    }

    @Override
    public long ticketHeadroomMiB(ServerState server) {
        return server.preTicketHeadroomMiB();
    }
}
