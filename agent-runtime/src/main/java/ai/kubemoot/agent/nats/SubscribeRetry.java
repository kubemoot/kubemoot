package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.logging.LogContent;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import io.nats.client.Connection;
import io.nats.client.Dispatcher;
import io.nats.client.JetStreamApiException;

import java.io.IOException;
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
 * never run again) or when {@link #stop()} is called; after either, the instance is spent.
 */
final class SubscribeRetry {

    private static final Logger log = LoggerFactory.getLogger(SubscribeRetry.class);

    static final Duration DEFAULT_FIRST_WAIT = Duration.ofSeconds(1);
    static final Duration DEFAULT_MAX_WAIT = Duration.ofSeconds(30);

    private final String what;
    private final Duration first;
    private final Duration max;
    private ScheduledThreadPoolExecutor executor;
    private boolean stopped;

    SubscribeRetry(String what) {
        this(what, DEFAULT_FIRST_WAIT, DEFAULT_MAX_WAIT);
    }

    SubscribeRetry(String what, Duration first, Duration max) {
        this.what = what;
        this.first = first.isZero() || first.isNegative() ? DEFAULT_FIRST_WAIT : first;
        this.max = max.compareTo(this.first) < 0 ? this.first : max;
    }

    /** What a subscribe attempt does on its new dispatcher; it may throw. */
    @FunctionalInterface
    interface DispatcherSetup {
        void run(Dispatcher dispatcher) throws IOException, JetStreamApiException;
    }

    /**
     * Creates a dispatcher on {@code conn} and runs {@code setup} on it: the subscriptions and
     * whatever must start only once they exist. When setup does not finish (it throws, or an
     * Error escapes it) the dispatcher is closed, so the retry that follows cannot leave a
     * second set of subscriptions delivering every message twice.
     *
     * @return true when setup finished; false when it threw an exception (logged)
     */
    static boolean onNewDispatcher(Connection conn, String what, DispatcherSetup setup) {
        Dispatcher dispatcher = conn.createDispatcher();
        boolean done = false;
        try {
            setup.run(dispatcher);
            done = true;
        } catch (Exception e) {
            log.warn("{} subscribe failed: {}", what, LogContent.reason(e));
        } finally {
            if (!done) {
                conn.closeDispatcher(dispatcher);
            }
        }
        return done;
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

    /**
     * One scheduled attempt. Nothing escapes it: an executor drops the next run of a task
     * that threw, so even an Error from the attempt reschedules the retry.
     */
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
        } catch (Throwable e) { // NOSONAR: a retry loop must survive any failure of an attempt
            log.warn("{} subscribe attempt failed: {}", what, LogContent.reason(e));
            return false;
        }
    }
}
