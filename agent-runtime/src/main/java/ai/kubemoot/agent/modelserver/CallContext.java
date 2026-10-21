package ai.kubemoot.agent.modelserver;

/**
 * Where a call was placed, known before it is sent: the server and model, which
 * path sends it, whether the model was resident there when the call was placed, and
 * how many calls held a ticket on the server when it started (this one included).
 */
public record CallContext(String server, String model, String kind, boolean coldStart, int activeCalls,
                          PlacedCall call) {

    /** A context for a call with no placement decision to score. */
    public CallContext(String server, String model, String kind, boolean coldStart, int activeCalls) {
        this(server, model, kind, coldStart, activeCalls, null);
    }

    /** This context with {@code coldStart} replaced. */
    public CallContext withColdStart(boolean cold) {
        return cold == coldStart ? this : new CallContext(server, model, kind, cold, activeCalls, call);
    }

    /**
     * Fewer output tokens than this give no generation time: the first token is not
     * timed as generation, so a short reply's rate would be badly overstated.
     */
    static final long MIN_TIMED_TOKENS = 8;

    /**
     * A streamed call's observation. Generation time is what followed the first token,
     * recorded when the reply has at least {@link #MIN_TIMED_TOKENS} tokens. Time to the
     * first token is prompt reading only when the model was already loaded, so it is
     * prompt time for a warm call and unknown for a cold one; load time is not separable
     * from the stream. With thinking on, the first token is a thinking token and
     * generation includes the thinking, as the output token count does.
     */
    public CallObservation streamed(boolean ok, long atMs, StreamedChat.Timed timed, long promptTokens,
                                    long outputTokens) {
        long generate = ok && outputTokens >= MIN_TIMED_TOKENS ? timed.generateMs() : CallObservation.UNKNOWN;
        long prompt = ok && !coldStart ? timed.firstTokenMs() : CallObservation.UNKNOWN;
        return new CallObservation(server, model, kind, ok, coldStart, promptTokens, outputTokens, timed.totalMs(),
                CallObservation.UNKNOWN, prompt, generate, activeCalls, atMs, ok ? timed.firstTokenMs() : CallObservation.UNKNOWN);
    }

    /** The observation of one request sent in this context; engine timings unknown. */
    public CallObservation measured(boolean ok, long atMs, long durationMs, long promptTokens, long outputTokens) {
        return new CallObservation(server, model, kind, ok, coldStart, promptTokens, outputTokens, durationMs,
                CallObservation.UNKNOWN, CallObservation.UNKNOWN, CallObservation.UNKNOWN, activeCalls, atMs);
    }
}
