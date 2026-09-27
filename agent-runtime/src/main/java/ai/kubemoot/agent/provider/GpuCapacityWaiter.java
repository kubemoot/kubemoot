package ai.kubemoot.agent.provider;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.Optional;
import java.util.function.Supplier;

/**
 * Waits for GPU capacity when every GPU that could hold a model is busy.
 *
 * <p>The wait is state-driven: after each failed pick the waiter blocks on the
 * {@link CapacitySignal}, which advances on every change to the provider-state
 * and ticket KV buckets (a probe publishing new residency, a ticket released)
 * and when the discussion ends. Each change triggers one more pick attempt. The
 * normal exit is a successful claim.</p>
 *
 * <p>Two exits give up with {@link NoFitException#REASON_GPU_BUSY}: the thread
 * ended, or the wait reached its safety limit
 * ({@code kubemoot.discuss.gpu-wait-limit-seconds}, default the discussion
 * synthesis timeout). The limit is a safety net for a lost wake-up, not the way
 * a wait normally ends.</p>
 */
@ApplicationScoped
public class GpuCapacityWaiter {

    private static final Logger log = LoggerFactory.getLogger(GpuCapacityWaiter.class);

    private final CapacitySignal signal;
    private final Duration limit;

    @Inject
    public GpuCapacityWaiter(CapacitySignal signal,
                             @ConfigProperty(name = "kubemoot.discuss.gpu-wait-limit-seconds", defaultValue = "0")
                             int limitSeconds,
                             @ConfigProperty(name = "kubemoot.discuss.synthesis-timeout-seconds", defaultValue = "90")
                             int synthesisTimeoutSeconds) {
        this.signal = signal;
        this.limit = Duration.ofSeconds(limitSeconds > 0 ? limitSeconds : synthesisTimeoutSeconds);
    }

    /** The safety limit on one wait. */
    public Duration limit() {
        return limit;
    }

    /**
     * Retries {@code attempt} on every capacity change until it yields a value.
     * Publishes {@link CapacityWait#onWaiting} once, before the first block, and
     * {@link CapacityWait#onCapacity} when an announced wait succeeds.
     *
     * @throws NoFitException with reason gpu-busy when the thread ends, the
     *         capacity watch cannot start, or the safety limit passes
     */
    public <T> T await(String model, Supplier<Optional<T>> attempt, CapacityWait wait) {
        if (wait.threadEnded()) {
            throw NoFitException.gpuBusy(model, "no GPU has room and this call cannot wait");
        }
        if (!signal.ensureWatching()) {
            throw NoFitException.gpuBusy(model, "no GPU has room and the capacity watch is unavailable");
        }
        wait.wakeOnEnd(signal::nudge);
        long deadline = System.nanoTime() + limit.toNanos();
        boolean announced = false;
        boolean changed = true;
        while (changed) {
            long seen = signal.version();
            Optional<T> result = attempt.get();
            if (result.isPresent()) {
                return finish(model, wait, announced, result.get());
            }
            if (wait.threadEnded()) {
                throw NoFitException.gpuBusy(model, "the discussion ended while every GPU was busy");
            }
            if (!announced) {
                log.info("Every GPU that can hold {} is busy - waiting for capacity (limit {}s)",
                        model, limit.toSeconds());
                wait.onWaiting(model);
                announced = true;
            }
            changed = blockUntilChange(model, seen, deadline);
        }
        return lastAttempt(model, attempt, wait);
    }

    /** One more try once the limit has passed without a change; gpu-busy when it still finds no room. */
    private <T> T lastAttempt(String model, Supplier<Optional<T>> attempt, CapacityWait wait) {
        Optional<T> result = attempt.get();
        if (result.isPresent()) {
            return finish(model, wait, true, result.get());
        }
        throw NoFitException.gpuBusy(model, "every GPU stayed busy for the " + limit.toSeconds() + "s wait limit");
    }

    private <T> T finish(String model, CapacityWait wait, boolean announced, T value) {
        if (announced) {
            log.info("GPU capacity arrived for {}", model);
            wait.onCapacity(model);
        }
        return value;
    }

    /**
     * Blocks until capacity state changes or the deadline passes. Returns false when
     * the deadline passed without a change; the signal's answer is the one clock.
     */
    private boolean blockUntilChange(String model, long seen, long deadline) {
        long remainingNanos = deadline - System.nanoTime();
        if (remainingNanos <= 0) {
            return false;
        }
        try {
            return signal.awaitChange(seen, Duration.ofNanos(remainingNanos));
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw NoFitException.gpuBusy(model, "interrupted while waiting for a GPU");
        }
    }
}
