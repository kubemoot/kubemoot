package ai.kubemoot.agent.provider;

/**
 * Where a call was placed, known before it is sent: the provider and model, which
 * path sends it, whether the model was resident there when the call was placed, and
 * how many calls held a ticket on the provider when it started (this one included).
 */
public record CallContext(String provider, String model, String kind, boolean coldStart, int activeCalls) {

    /** This context with {@code coldStart} replaced. */
    public CallContext withColdStart(boolean cold) {
        return cold == coldStart ? this : new CallContext(provider, model, kind, cold, activeCalls);
    }

    /** The observation of one request sent in this context; engine timings unknown. */
    public CallObservation measured(boolean ok, long atMs, long durationMs, long promptTokens, long outputTokens) {
        return new CallObservation(provider, model, kind, ok, coldStart, promptTokens, outputTokens, durationMs,
                CallObservation.UNKNOWN, CallObservation.UNKNOWN, CallObservation.UNKNOWN, activeCalls, atMs);
    }
}
