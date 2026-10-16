package ai.kubemoot.agent.provider;

/**
 * What one model request did on one provider, as the scheduler observed it.
 *
 * <p>Times are milliseconds; an engine timing the engine did not report is
 * {@value #UNKNOWN}. {@code durationMs} is measured by the agent and includes
 * any wait inside the engine; {@code loadMs}, {@code promptEvalMs} and
 * {@code evalMs} come from the engine when it reports them.</p>
 *
 * @param provider     the ModelProvider the request ran on
 * @param model        the model it ran
 * @param kind         which path sent it: {@link #KIND_TOOL_TURN}, {@link #KIND_SIMPLE} or {@link #KIND_TRIAGE}
 * @param ok           false when the request failed or timed out
 * @param coldStart    the model was not resident on the provider when the call was placed
 * @param promptTokens prompt tokens the engine reported (0 when unknown)
 * @param outputTokens generated tokens the engine reported (0 when unknown)
 * @param durationMs   time from sending the request to the reply or failure
 * @param loadMs       model load time the engine reported
 * @param promptEvalMs prompt processing time the engine reported
 * @param evalMs       generation time the engine reported
 * @param activeCalls  calls holding a ticket on the provider when the call started, this one included
 * @param atMs         when the request started, epoch milliseconds
 */
public record CallObservation(String provider, String model, String kind, boolean ok, boolean coldStart,
                              long promptTokens, long outputTokens, long durationMs,
                              long loadMs, long promptEvalMs, long evalMs,
                              int activeCalls, long atMs) {

    /** An engine timing that was not reported. */
    public static final long UNKNOWN = -1L;

    /** One model turn of the tool loop. */
    public static final String KIND_TOOL_TURN = "tool-turn";
    /** A no-tool call: advisory or synthesis. */
    public static final String KIND_SIMPLE = "simple";
    /** A triage call. */
    public static final String KIND_TRIAGE = "triage";

    /**
     * This observation with the token counts and engine timings from an Ollama
     * {@code /api/chat} reply ({@code prompt_eval_count}, {@code eval_count}, and the
     * {@code *_duration} fields in nanoseconds); unchanged when there is no reply.
     */
    public CallObservation withOllamaReply(com.fasterxml.jackson.databind.JsonNode reply) {
        if (reply == null || reply.isMissingNode() || reply.isNull()) {
            return this;
        }
        return new CallObservation(provider, model, kind, ok, coldStart,
                reply.path("prompt_eval_count").asLong(promptTokens),
                reply.path("eval_count").asLong(outputTokens), durationMs,
                nanosToMs(reply, "load_duration"), nanosToMs(reply, "prompt_eval_duration"),
                nanosToMs(reply, "eval_duration"), activeCalls, atMs);
    }

    private static long nanosToMs(com.fasterxml.jackson.databind.JsonNode reply, String field) {
        var n = reply.path(field);
        return n.isNumber() ? Math.round(n.asLong() / 1_000_000.0) : UNKNOWN;
    }

    /** True when the observation names a provider and a model, so it can be kept. */
    public boolean attributable() {
        return names(provider, model);
    }

    /** True when both a provider and a model are named. */
    public static boolean names(String provider, String model) {
        return isNamed(provider) && isNamed(model);
    }

    private static boolean isNamed(String s) {
        return s != null && !s.isEmpty();
    }
}
