package ai.kubemoot.agent.provider;

/**
 * Thrown by the chat pipeline when no GPU can serve this call, so the agent
 * stands aside instead of calling a provider the scheduler refused.
 *
 * <h3>Reasons</h3>
 * <ul>
 *   <li>{@link #REASON_GPU_BUSY}: some GPU could hold the model, but every one
 *       stayed busy with other work until the discussion ended or the agent's
 *       capacity wait reached its safety limit.</li>
 *   <li>{@link #REASON_MODEL_TOO_LARGE}: no GPU in the cluster has enough usable
 *       VRAM to ever hold the model.</li>
 * </ul>
 *
 * <h3>Behavior</h3>
 * {@code DiscussionSubscriber.runMullingPhase} catches this and publishes a
 * {@code stand_aside} signal with {@code metadata.reason} set to {@link #reason()}
 * and {@code metadata.model} set to {@link #model()}. The coordinator records the
 * reason so its answer can tell the crew designer the cluster, not the crew,
 * was the limit.
 *
 * <h3>Not a failure</h3>
 * The agent is not broken: it was selected and willing to answer, but no GPU
 * could run it. A {@code stand_aside} with a reason says exactly that, distinct
 * from a {@code failure} signal (something went wrong during the call).
 */
public class NoFitException extends RuntimeException {

    /** Stand-aside reason: every GPU that could hold the model stayed busy. */
    public static final String REASON_GPU_BUSY = "gpu-busy";

    /** Stand-aside reason: no GPU in the cluster can ever hold the model. */
    public static final String REASON_MODEL_TOO_LARGE = "model-too-large";

    private final String predictorReason;
    private final String reason;
    private final String model;

    /** A gpu-busy refusal with no model recorded. */
    public NoFitException(String predictorReason) {
        this(REASON_GPU_BUSY, "", predictorReason);
    }

    public NoFitException(String reason, String model, String predictorReason) {
        super("no provider currently fits this call (" + reason + "): " + predictorReason);
        this.reason = reason;
        this.model = model == null ? "" : model;
        this.predictorReason = predictorReason;
    }

    /** Every GPU that could hold {@code model} stayed busy. */
    public static NoFitException gpuBusy(String model, String detail) {
        return new NoFitException(REASON_GPU_BUSY, model, detail);
    }

    /** No GPU can ever hold {@code model}. */
    public static NoFitException modelTooLarge(String model, String detail) {
        return new NoFitException(REASON_MODEL_TOO_LARGE, model, detail);
    }

    /** The scheduler's reasoning for the refusal, for logs and signal metadata. */
    public String predictorReason() {
        return predictorReason;
    }

    /** {@link #REASON_GPU_BUSY} or {@link #REASON_MODEL_TOO_LARGE}. */
    public String reason() {
        return reason;
    }

    /** The model the agent needed; empty when unknown. */
    public String model() {
        return model;
    }
}
