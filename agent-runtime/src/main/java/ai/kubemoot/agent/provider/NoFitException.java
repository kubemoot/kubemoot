package ai.kubemoot.agent.provider;

/**
 * Thrown by the chat pipeline when {@link FitPredictor} refuses every
 * candidate provider AND NATS is healthy (so the empty selection is
 * authoritative, not an infrastructure outage).
 *
 * <h3>Why a distinct exception</h3>
 * Pre-Phase-E, an empty {@link ProviderSelector#pickAndClaim} result
 * caused {@code ChatService} to fall back to the statically-injected
 * {@code chatModel} (whose endpoint comes from the operator's reconcile-
 * time assignment). On a provider the predictor JUST refused — e.g. a
 * rig1 that Phase D's KV-cache gate excluded because the call would
 * push VRAM over capacity — the static endpoint is the SAME provider,
 * so the fallback re-enters the exact failure mode the gate was
 * supposed to prevent. Observed concretely on thread 0589b246-…
 * (2026-05-28): obs-metrics + nvidia-gpu both refused by KV gate,
 * both fell back to their static rig1 endpoint, both wedged Ollama
 * exactly like d61647e9.
 *
 * <h3>Behavior</h3>
 * {@code DiscussionSubscriber.runMullingPhase} catches this and
 * publishes a {@code stand_aside} signal with {@code metadata.reason =
 * "no-fit"} instead of attempting the call. The coordinator sees the
 * stand-aside and settles fast; user doesn't get this agent's view but
 * also doesn't wait 6+ minutes for a timeout. Faster, safer, honest.
 *
 * <h3>Not a failure</h3>
 * The agent isn't broken — it WAS willing and able (relevance passed
 * triage), but every provider is currently full. {@code stand_aside}
 * with a reason is the right signal: explicitly different from a
 * {@code failure} which would indicate something went wrong.
 */
public class NoFitException extends RuntimeException {
    private final String predictorReason;

    public NoFitException(String predictorReason) {
        super("no provider currently fits this call: " + predictorReason);
        this.predictorReason = predictorReason;
    }

    /** The predictor's last reasoning string for the rejected candidate(s). */
    public String predictorReason() {
        return predictorReason;
    }
}
