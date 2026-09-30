package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.Timeout;

import java.util.List;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;

/** The GPU capacity wait: state-driven retries, one waiting announcement, gpu-busy exits. */
class GpuCapacityWaiterTest {

    private static final String MODEL = "qwen3:14b";

    /** An attempt that fails {@code failures} times, then yields "claimed". */
    private static java.util.function.Supplier<Optional<String>> succeedsAfter(int failures, AtomicInteger calls) {
        return () -> calls.incrementAndGet() > failures ? Optional.of("claimed") : Optional.empty();
    }

    @Test
    void immediateClaim_neverAnnouncesWaiting() {
        var signal = new FakeCapacitySignal();
        var wait = new RecordingCapacityWait();
        var waiter = new GpuCapacityWaiter(signal, 60, 90);

        assertEquals("claimed", waiter.await(MODEL, succeedsAfter(0, new AtomicInteger()), wait));
        assertTrue(wait.waiting.isEmpty());
        assertTrue(wait.capacity.isEmpty());
        assertEquals(0, signal.awaits.get());
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void resumesOnCapacityChange_andAnnouncesWaitingOnce() {
        var signal = new FakeCapacitySignal();
        var calls = new AtomicInteger();
        // Each announcement or retry is followed by a KV change from "another agent".
        var wait = new RecordingCapacityWait().whenWaiting(signal::nudge);
        var waiter = new GpuCapacityWaiter(signal, 60, 90);
        java.util.function.Supplier<Optional<String>> attempt = () -> {
            int n = calls.incrementAndGet();
            if (n > 1) {
                signal.nudge(); // a later change keeps the retries going without a timer
            }
            return n > 3 ? Optional.of("claimed") : Optional.empty();
        };

        assertEquals("claimed", waiter.await(MODEL, attempt, wait));
        assertEquals(List.of(MODEL), wait.waiting, "waiting is published once, however many changes arrive");
        assertEquals(List.of(MODEL), wait.capacity, "capacity arrival is announced after an announced wait");
        assertEquals(4, calls.get());
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void blocksUntilAKvChange_thenRetries() throws Exception {
        var signal = new FakeCapacitySignal();
        var calls = new AtomicInteger();
        var wait = new RecordingCapacityWait();
        var waiter = new GpuCapacityWaiter(signal, 60, 90);

        var result = CompletableFuture.supplyAsync(() -> waiter.await(MODEL, succeedsAfter(1, calls), wait));
        while (wait.waiting.isEmpty()) {
            Thread.onSpinWait();
        }
        assertThrows(TimeoutException.class, () -> result.get(100, TimeUnit.MILLISECONDS),
                "no change arrived, so the waiter is still blocked");
        assertEquals(1, calls.get(), "no retry without a change");

        signal.nudge();
        assertEquals("claimed", result.get(5, TimeUnit.SECONDS));
        assertEquals(2, calls.get());
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void threadEnd_givesUpWithGpuBusy() throws Exception {
        var signal = new FakeCapacitySignal();
        var wait = new RecordingCapacityWait();
        var waiter = new GpuCapacityWaiter(signal, 60, 90);

        var result = CompletableFuture.supplyAsync(() -> waiter.await(MODEL, Optional::<String>empty, wait));
        while (wait.waiting.isEmpty()) {
            Thread.onSpinWait();
        }
        wait.endThread();

        var ex = assertThrows(java.util.concurrent.ExecutionException.class, () -> result.get(5, TimeUnit.SECONDS));
        var nfe = assertInstanceOf(NoFitException.class, ex.getCause());
        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertEquals(MODEL, nfe.model());
        assertTrue(nfe.predictorReason().contains("discussion ended"));
    }

    @Test
    @Timeout(value = 10, unit = TimeUnit.SECONDS)
    void safetyLimit_givesUpWithGpuBusy_afterOneLastAttempt() {
        var signal = new FakeCapacitySignal();
        var calls = new AtomicInteger();
        var wait = new RecordingCapacityWait();
        var waiter = new GpuCapacityWaiter(signal, 1, 90);

        long start = System.nanoTime();
        var nfe = assertThrows(NoFitException.class,
                () -> waiter.await(MODEL, succeedsAfter(Integer.MAX_VALUE, calls), wait));
        long elapsedMs = (System.nanoTime() - start) / 1_000_000L;

        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertTrue(nfe.predictorReason().contains("wait limit"));
        assertTrue(elapsedMs >= 900, "the limit is the safety net: " + elapsedMs + "ms");
        assertEquals(2, calls.get(), "one attempt before blocking, one after the limit passed");
        assertEquals(1, wait.waiting.size());
    }

    @Test
    void callThatCannotWait_givesUpAtOnce() {
        var calls = new AtomicInteger();
        var nfe = assertThrows(NoFitException.class, () -> new GpuCapacityWaiter(new FakeCapacitySignal(), 60, 90)
                .await(MODEL, succeedsAfter(0, calls), CapacityWait.NONE));
        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertEquals(0, calls.get(), "NONE never retries");
    }

    @Test
    void unavailableWatch_givesUpInsteadOfWaitingBlind() {
        var wait = new RecordingCapacityWait();
        var nfe = assertThrows(NoFitException.class, () -> new GpuCapacityWaiter(new FakeCapacitySignal(false), 60, 90)
                .await(MODEL, Optional::<String>empty, wait));
        assertEquals(NoFitException.REASON_GPU_BUSY, nfe.reason());
        assertTrue(wait.waiting.isEmpty());
    }

    @Test
    void limit_defaultsToSynthesisTimeout_whenUnset() {
        assertEquals(90, new GpuCapacityWaiter(new FakeCapacitySignal(), 0, 90).limit().toSeconds());
        assertEquals(45, new GpuCapacityWaiter(new FakeCapacitySignal(), 45, 90).limit().toSeconds());
        assertEquals(90, new GpuCapacityWaiter(new FakeCapacitySignal(), -5, 90).limit().toSeconds());
    }
}
