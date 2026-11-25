package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ScheduledThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.function.BooleanSupplier;

/**
 * Keeps trying to subscribe until it works. A subscriber that starts while NATS is
 * unreachable (after a power loss NATS waits for its volume) would otherwise subscribe
 * once, fail, and stay idle for the life of the process. Attempts back off by doubling
 * from {@code first} to {@code max}; an attempt that throws counts as a failed attempt
 * and does not end the retries. The retries end at the first success (the attempt is
 * never run again) or when {@link #stop()} is called.
 */
final class SubscribeRetry {

    private static final Logger log = LoggerFactory.getLogger(SubscribeRetry.class);

    static final Duration FIRST = Duration.ofSeconds(1);
    static final Duration MAX = Duration.ofSeconds(30);

    private final String what;
    private final Duration first;
    private final Duration max;
    private ScheduledThreadPoolExecutor executor;
    private boolean stopped;

    SubscribeRetry(String what) {
        this(what, FIRST, MAX);
    }

    SubscribeRetry(String what, Duration first, Duration max) {
        this.what = what;
        this.first = first.isZero() || first.isNegative() ? FIRST : first;
        this.max = max.compareTo(this.first) < 0 ? this.first : max;
    }

    /** The wait before the first retry. */
    Duration first() {
        return first;
    }

    /** The wait after {@code waited}: doubled, never more than {@code max}. */
    Duration nextDelay(Duration waited) {
        var doubled = waited.multipliedBy(2);
        return doubled.compareTo(max) > 0 ? max : doubled;
    }

    /**
     * Runs {@code attempt} now, on the caller's thread; when it returns false or throws,
     * retries it in the background with backoff until it returns true.
     *
     * @return true when the first attempt succeeded
     */
    boolean attemptOrRetry(BooleanSupplier attempt) {
        if (isStopped()) {
            return false;
        }
        if (succeeded(attempt)) {
            return true;
        }
        schedule(attempt, first);
        return false;
    }

    /**
     * Ends the retries: queued retries are dropped and an attempt already running finishes
     * (it is not interrupted). Safe to call more than once.
     */
    synchronized void stop() {
        stopped = true;
        if (executor != null) {
            executor.setExecuteExistingDelayedTasksAfterShutdownPolicy(false);
            executor.shutdown();
        }
    }

    private synchronized boolean isStopped() {
        return stopped;
    }

    /** Test seam: whether the retry thread has ended after {@link #stop()}. */
    boolean isTerminatedWithinForTest(Duration d) throws InterruptedException {
        ScheduledThreadPoolExecutor current;
        synchronized (this) {
            current = executor;
        }
        return current == null || current.awaitTermination(d.toMillis(), TimeUnit.MILLISECONDS);
    }

    private synchronized void schedule(BooleanSupplier attempt, Duration wait) {
        if (stopped) {
            return;
        }
        if (executor == null) {
            executor = new ScheduledThreadPoolExecutor(1, r -> {
                Thread t = new Thread(r, "subscribe-retry-" + what);
                t.setDaemon(true);
                return t;
            });
        }
        try {
            executor.schedule(() -> runOnce(attempt, wait), wait.toMillis(), TimeUnit.MILLISECONDS);
        } catch (RejectedExecutionException e) {
            log.debug("{} retry not scheduled: stopped", what);
        }
    }

    private void runOnce(BooleanSupplier attempt, Duration waited) {
        if (succeeded(attempt)) {
            log.info("{} subscribed after retrying", what);
            stop();
            return;
        }
        schedule(attempt, nextDelay(waited));
    }

    private boolean succeeded(BooleanSupplier attempt) {
        try {
            return attempt.getAsBoolean();
        } catch (RuntimeException e) {
            log.warn("{} subscribe attempt failed: {}", what, LogContent.reason(e));
            return false;
        }
    }
}
