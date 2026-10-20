package ai.kubemoot.agent.modelserver;

/**
 * How placement judges a server, decided by the server's engine. Each engine has
 * its own answer to three questions the scheduler asks of every candidate:
 *
 * <ul>
 *   <li>Can the server serve the model at all ({@link #predict})? Ollama loads a
 *       model on demand, so the answer is a question of accelerator memory. A server
 *       that reserved its memory when it started (vLLM) serves the models it hosts,
 *       whatever their size.</li>
 *   <li>How crowded is it ({@link #contention})? Ollama has fixed parallel slots; an
 *       engine that batches requests reports its own queue and cache use.</li>
 *   <li>How much accelerator memory can a call's ticket claim ({@link #ticketHeadroomMiB})?
 *       Tickets count in-flight calls; for an engine that budgets no memory they must
 *       never refuse a call.</li>
 * </ul>
 *
 * <p>{@link EngineFits#forType} picks the implementation. {@link OllamaFit} keeps the
 * memory rules the scheduler was built on, unchanged.</p>
 */
public interface EngineFit {

    /** Whether {@code in.server()} can serve {@code in.modelName()}, and why. Pure. */
    FitScore predict(FitInputs in);

    /**
     * True when the scheduler counts accelerator memory for this engine's models: what is
     * resident, what a load needs, what an eviction frees. False when the engine reserved
     * its memory when its server started, so none of those questions apply.
     */
    boolean budgetsMemory();

    /**
     * How crowded the server is, in the units of one full parallel slot on Ollama (a
     * value of 1.0 is one call queued per slot). Lower is better when ranking servers.
     */
    double contention(ServerState server, int inFlightCalls);

    /** The accelerator memory, in MiB, a ticket claim on the server may count against. */
    long ticketHeadroomMiB(ServerState server);
}
