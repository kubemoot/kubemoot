package ai.kubemoot.agent.chat;

/**
 * Characters per token for this agent's prompts, learned from the prompt sizes
 * the engine reports. Tokenizers differ by model family, and prompts differ by
 * content (prose runs near 4 characters per token, JSON and code nearer 3), so a
 * fixed ratio either refuses prompts that fit or admits prompts the engine cuts.
 *
 * <p>Ollama's reported prompt size counts every token of the prompt, including a
 * prefix reused from cache, so each report is a full measurement. Until the
 * first report arrives, {@link #DEFAULT} applies.</p>
 */
final class CharsPerToken {

    /** Ratio used before any measurement: between prose and JSON. */
    static final double DEFAULT = 3.5;

    /** Weight of the newest measurement. */
    static final double ALPHA = 0.3;

    /**
     * Characters per token no estimate goes above. JSON and YAML tool output runs
     * near 3, so text is never counted as fewer tokens than this, even when the
     * measured ratio (taken over mostly prose prompts) is higher.
     */
    static final double DENSE = 3.0;

    /** Measurements outside this range are not a prompt the engine tokenized normally. */
    static final double MIN = 1.0;
    static final double MAX = 8.0;

    private volatile double ratio = DEFAULT;
    private volatile boolean measured;

    /** Record that a prompt of {@code chars} characters was {@code tokens} tokens. */
    void observe(long chars, long tokens) {
        if (chars <= 0 || tokens <= 0) {
            return;
        }
        double sample = (double) chars / tokens;
        if (sample < MIN || sample > MAX) {
            return;
        }
        synchronized (this) {
            ratio = measured ? ratio + ALPHA * (sample - ratio) : sample;
            measured = true;
        }
    }

    /** The ratio the estimate uses: the measured ratio, or {@link #DENSE} when that is higher. */
    double estimateRatio() {
        return Math.min(ratio, DENSE);
    }

    /** The current measured ratio. */
    double ratio() {
        return ratio;
    }

    /**
     * Estimated tokens in {@code chars} characters of prompt, counted at the denser of the
     * measured ratio and {@link #DENSE} (the larger token count), so the estimate never
     * runs under what the engine counts for dense text.
     */
    long tokens(long chars) {
        return chars <= 0 ? 0 : (long) Math.ceil(chars / estimateRatio());
    }
}
