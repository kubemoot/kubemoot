package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The count of the coordinator's model calls in flight, which tests wait on instead of a quiet period. */
class LlmTaskCountTest extends CoordinatorThreadTestBase {

    @Test
    void aCallIsCountedWhileItRunsAndNotAfter() throws Exception {
        var started = new CountDownLatch(1);
        var release = new CountDownLatch(1);

        orchestrator.submitLlm(() -> {
            started.countDown();
            try {
                release.await(10, TimeUnit.SECONDS);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        });

        assertTrue(started.await(10, TimeUnit.SECONDS));
        assertEquals(1, orchestrator.llmTasksInFlight(), "counted while it runs");
        release.countDown();
        await(() -> orchestrator.llmTasksInFlight() == 0, "the count did not return to zero");
    }

    @Test
    void aCallThatThrowsDoesNotLeaveACount() throws Exception {
        orchestrator.submitLlm(() -> {
            throw new IllegalStateException("the model call failed");
        });

        await(() -> orchestrator.llmTasksInFlight() == 0, "a failed call stayed counted");
    }

    @Test
    void aCallTheExecutorRefusesDoesNotLeaveACount() {
        orchestrator.stopLlmExecutor();

        assertThrows(RejectedExecutionException.class, () -> orchestrator.submitLlm(() -> { }));

        assertEquals(0, orchestrator.llmTasksInFlight(), "a refused call is not counted");
    }

    @Test
    void severalCallsAreCountedTogether() throws Exception {
        var release = new CountDownLatch(1);
        for (int i = 0; i < 3; i++) {
            orchestrator.submitLlm(() -> {
                try {
                    release.await(10, TimeUnit.SECONDS);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                }
            });
        }

        assertEquals(3, orchestrator.llmTasksInFlight());
        release.countDown();
        await(() -> orchestrator.llmTasksInFlight() == 0, "the counts did not return to zero");
    }
}
