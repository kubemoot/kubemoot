package ai.kubemoot.agent.chat;

import java.util.ArrayDeque;
import java.util.Deque;

/**
 * How far this agent's tool loops grow past their first prompt, in tokens,
 * learned from the loops it has run. A tool loop's prompt grows with every tool
 * result and every reply, so the context a call needs is its first prompt plus
 * that growth, not the first prompt alone.
 *
 * <p>Each loop reports its first prompt and the largest prompt it reached or
 * would have sent next. The estimate is the largest growth among the most recent
 * {@link #WINDOW} loops, so one unusually small loop does not shrink it. Before
 * any loop has been observed, the caller's default applies.</p>
 */
final class LoopGrowth {

    /** Number of recent loops the estimate covers. */
    static final int WINDOW = 16;

    private final Deque<Long> samples = new ArrayDeque<>();

    /**
     * Record one loop. Ignored when either size is unknown (zero or negative); a
     * loop that never grew records zero growth.
     */
    synchronized void record(long firstPromptTokens, long peakTokens) {
        if (firstPromptTokens <= 0 || peakTokens <= 0) {
            return;
        }
        samples.addLast(Math.max(0L, peakTokens - firstPromptTokens));
        while (samples.size() > WINDOW) {
            samples.removeFirst();
        }
    }

    /** The expected growth in tokens, or {@code unobservedDefault} before any loop was recorded. */
    synchronized long estimate(long unobservedDefault) {
        if (samples.isEmpty()) {
            return Math.max(0L, unobservedDefault);
        }
        long largest = 0L;
        for (long sample : samples) {
            largest = Math.max(largest, sample);
        }
        return largest;
    }
}
