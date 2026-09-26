package ai.kubemoot.agent.nats;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Rate limiter for discussion contributions.
 * - Max N contributions per thread per tooler (default 3, configurable)
 * - Sliding window: maxInferencesPerMinute (default 10)
 */
public class DiscussionRateLimiter {

    private static final Logger log = LoggerFactory.getLogger(DiscussionRateLimiter.class);

    // Sliding-window length (ms) for the per-minute inference rate cap.
    private static final long RATE_WINDOW_MS = 60_000;
    // Distinct-thread cap before the bookkeeping map is cleared wholesale to bound memory.
    private static final int MAX_TRACKED_THREADS = 1000;

    private final ConcurrentHashMap<String, AtomicInteger> threadCounts = new ConcurrentHashMap<>();
    private final AtomicInteger minuteCount = new AtomicInteger(0);
    private final AtomicLong windowStart = new AtomicLong(System.currentTimeMillis());
    private final int maxPerMinute;
    private final int maxPerThread;

    public DiscussionRateLimiter() {
        this(10, 3);
    }

    public DiscussionRateLimiter(int maxPerMinute, int maxPerThread) {
        this.maxPerMinute = maxPerMinute <= 0 ? Integer.MAX_VALUE : maxPerMinute;
        this.maxPerThread = maxPerThread <= 0 ? 3 : maxPerThread;
    }

    public boolean tryAcquire(String threadId) {
        var counter = threadCounts.computeIfAbsent(threadId, k -> new AtomicInteger(0));
        int count = counter.incrementAndGet();
        if (count > maxPerThread) {
            log.debug("Thread {} contribution limit reached: {}/{}", threadId, count, maxPerThread);
            counter.decrementAndGet();
            return false;
        }

        long now = System.currentTimeMillis();
        long start = windowStart.get();
        if (now - start > RATE_WINDOW_MS) {
            windowStart.set(now);
            minuteCount.set(1);
            return true;
        }

        int minuteTotal = minuteCount.incrementAndGet();
        if (minuteTotal > maxPerMinute) {
            log.warn("Rate limit exceeded: {}/{} inferences per minute", minuteTotal, maxPerMinute);
            counter.decrementAndGet();
            minuteCount.decrementAndGet();
            return false;
        }

        return true;
    }

    public int getThreadCount(String threadId) {
        var counter = threadCounts.get(threadId);
        return counter != null ? counter.get() : 0;
    }

    public void cleanup() {
        if (threadCounts.size() > MAX_TRACKED_THREADS) {
            threadCounts.clear();
        }
    }
}
