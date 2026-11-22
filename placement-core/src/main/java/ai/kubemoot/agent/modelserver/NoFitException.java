package ai.kubemoot.agent.modelserver;

/**
 * Thrown by the chat pipeline when no accelerator can serve this call, so the agent
 * stands aside instead of calling a server the scheduler refused.
 *
 * <h3>Reasons</h3>
 * <ul>
 *   <li>{@link #REASON_ACCELERATOR_BUSY}: some accelerator could hold the model, but every one
 *       stayed busy with other work until the discussion ended or the agent's
 *       capacity wait reached its safety limit.</li>
 *   <li>{@link #REASON_MODEL_TOO_LARGE}: no accelerator in the cluster has enough usable
 *       memory to ever hold the model.</li>
 *   <li>{@link #REASON_PROMPT_TOO_LARGE}: the prompt is larger than the context
 *       window every server gives the model, so the engine would drop part of
 *       it (the question among it) instead of answering it.</li>
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
 * The agent is not broken: it was selected and willing to answer, but no accelerator
 * could run it. A {@code stand_aside} with a reason says exactly that, distinct
 * from a {@code failure} signal (something went wrong during the call).
 */
public class NoFitException extends RuntimeException {

    /** Stand-aside reason: every accelerator that could hold the model stayed busy. */
    public static final String REASON_ACCELERATOR_BUSY = "accelerator-busy";

    /**
     * The old value of {@link #REASON_ACCELERATOR_BUSY}. Publishers no longer send it; readers
     * accept it for one release, from a publisher older than this release.
     */
    public static final String REASON_GPU_BUSY_DEPRECATED = "gpu-busy";

    /** Stand-aside reason: no accelerator in the cluster can ever hold the model. */
    public static final String REASON_MODEL_TOO_LARGE = "model-too-large";

    /** Stand-aside reason: the prompt is larger than every server's context for the model. */
    public static final String REASON_PROMPT_TOO_LARGE = "prompt-too-large";

    private final String predictorReason;
    private final String reason;
    private final String model;

    /** An accelerator-busy refusal with no model recorded. */
    public NoFitException(String predictorReason) {
        this(REASON_ACCELERATOR_BUSY, "", predictorReason);
    }

    public NoFitException(String reason, String model, String predictorReason) {
        super("no server currently fits this call (" + reason + "): " + predictorReason);
        this.reason = reason;
        this.model = model == null ? "" : model;
        this.predictorReason = predictorReason;
    }

    /** Every accelerator that could hold {@code model} stayed busy. */
    public static NoFitException acceleratorBusy(String model, String detail) {
        return new NoFitException(REASON_ACCELERATOR_BUSY, model, detail);
    }

    /** True for {@link #REASON_ACCELERATOR_BUSY} and its deprecated old value. */
    public static boolean isAcceleratorBusy(String reason) {
        return REASON_ACCELERATOR_BUSY.equals(reason) || REASON_GPU_BUSY_DEPRECATED.equals(reason);
    }

    /** The reason under its current name: the deprecated old value becomes {@link #REASON_ACCELERATOR_BUSY}. */
    public static String canonicalReason(String reason) {
        return REASON_GPU_BUSY_DEPRECATED.equals(reason) ? REASON_ACCELERATOR_BUSY : reason;
    }

    /** No accelerator can ever hold {@code model}. */
    public static NoFitException modelTooLarge(String model, String detail) {
        return new NoFitException(REASON_MODEL_TOO_LARGE, model, detail);
    }

    /** The prompt is larger than every server's context window for {@code model}. */
    public static NoFitException promptTooLarge(String model, String detail) {
        return new NoFitException(REASON_PROMPT_TOO_LARGE, model, detail);
    }

    /** The scheduler's reasoning for the refusal, for logs and signal metadata. */
    public String predictorReason() {
        return predictorReason;
    }

    /** {@link #REASON_ACCELERATOR_BUSY}, {@link #REASON_MODEL_TOO_LARGE}, or {@link #REASON_PROMPT_TOO_LARGE}. */
    public String reason() {
        return reason;
    }

    /** The model the agent needed; empty when unknown. */
    public String model() {
        return model;
    }
}
