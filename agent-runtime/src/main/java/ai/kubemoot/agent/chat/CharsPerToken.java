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

    /** The current ratio. */
    double ratio() {
        return ratio;
    }

    /** Estimated tokens in {@code chars} characters of prompt. */
    long tokens(long chars) {
        return chars <= 0 ? 0 : (long) Math.ceil(chars / ratio);
    }
}
