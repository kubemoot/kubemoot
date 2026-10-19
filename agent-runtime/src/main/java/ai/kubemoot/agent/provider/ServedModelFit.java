package ai.kubemoot.agent.provider;

import java.util.List;

/**
 * An engine that serves the models its servers host and batches requests (vLLM, and any
 * other OpenAI-compatible server the runtime has no memory model for). The server
 * reserved its accelerator memory when it started, so:
 *
 * <ul>
 *   <li>a provider that hosts the model can serve it; a model's footprint, the memory
 *       total, and what else is resident do not enter the decision;</li>
 *   <li>capacity is the engine's own queue and cache, not a count of slots: there is room
 *       while the engine holds no waiting request and its key-value cache is not nearly
 *       full ({@link #hasFreeCapacity}). Calls the agents send join the engine's batch,
 *       so the number of calls in flight does not limit it;</li>
 *   <li>a ticket counts the call and never refuses it ({@link #ticketHeadroomMiB}).</li>
 * </ul>
 *
 * <p>A load the engine does not report (no request counts, no cache use) is read as no
 * load. A model that is hosted but asleep is served too: waking it is a cost the cold
 * candidate pays in the ranking, not a reason to refuse it.</p>
 */
final class ServedModelFit implements EngineFit {

    static final ServedModelFit INSTANCE = new ServedModelFit();

    /** Key-value cache use at or above which the engine has no room for another request. */
    static final double KV_CACHE_HIGH = 0.9;

    /**
     * Memory a ticket records for a call on this engine, and the footprint a placement
     * request carries for a model only such a provider hosts. It marks the call; it is
     * not memory the scheduler finds, because the engine already holds its memory.
     */
    static final long TICKET_MARKER_MIB = ProviderSelector.WARM_SCRATCH_MIB;

    /** A claim budget no ticket count can exceed. */
    static final long UNBOUNDED_HEADROOM_MIB = Long.MAX_VALUE / 4;

    /** Weight of a call this agent has in flight, to spread a burst before the engine's counts move. */
    private static final double LOCAL_CALL_WEIGHT = 0.01;

    private ServedModelFit() {}

    @Override
    public FitScore predict(FitInputs in) {
        if (in == null || in.provider() == null) {
            return FitScore.no("invalid inputs");
        }
        if (in.circuitOpen()) {
            return FitScore.no("circuit open");
        }
        if (!in.provider().ready()) {
            return FitScore.no("provider not ready");
        }
        if (!hosts(in)) {
            return FitScore.no("the provider does not host " + in.modelName());
        }
        String why = in.provider().hasModelLoaded(in.modelName())
                ? "hosted and awake (the server holds its memory)"
                : "hosted (the server holds its memory; waking it is a cost, not a refusal)";
        return FitScore.yes((long) in.observedLatencyMs(), why);
    }

    /** True when the provider lists the model, or a call holds a ticket on it there. */
    private static boolean hosts(FitInputs in) {
        return in.provider().hasModel(in.modelName())
                || (in.activeModelsOnProvider() != null && in.activeModelsOnProvider().contains(in.modelName()));
    }

    @Override
    public boolean budgetsMemory() {
        return false;
    }

    @Override
    public double contention(ProviderState provider, int inFlightCalls) {
        double queued = provider.requestsWaiting() == null ? 0.0 : provider.requestsWaiting();
        double cache = provider.kvCacheUsage() == null ? 0.0 : provider.kvCacheUsage();
        return queued + cache + LOCAL_CALL_WEIGHT * inFlightCalls;
    }

    @Override
    public long ticketHeadroomMiB(ProviderState provider) {
        return UNBOUNDED_HEADROOM_MIB;
    }

    /** True while the engine holds no waiting request and its cache is below {@link #KV_CACHE_HIGH}. */
    static boolean hasFreeCapacity(ProviderState provider) {
        boolean queued = provider.requestsWaiting() != null && provider.requestsWaiting() > 0;
        boolean cacheFull = provider.kvCacheUsage() != null && provider.kvCacheUsage() >= KV_CACHE_HIGH;
        return !queued && !cacheFull;
    }

    /**
     * Seconds until a new request starts: zero with room. Otherwise a waiting request
     * starts when a running one finishes, so the wait is one call's time for each
     * waiting request (at least one), spread across the running requests.
     */
    static double expectedWaitSeconds(ProviderState provider, List<Double> inFlightElapsedSeconds, double callSeconds) {
        if (hasFreeCapacity(provider)) {
            return 0.0;
        }
        int running = Math.max(1, provider.requestsRunning() == null ? inFlightElapsedSeconds.size()
                : provider.requestsRunning());
        int waiting = Math.max(1, provider.requestsWaiting() == null ? 0 : provider.requestsWaiting());
        return callSeconds * waiting / running;
    }
}
