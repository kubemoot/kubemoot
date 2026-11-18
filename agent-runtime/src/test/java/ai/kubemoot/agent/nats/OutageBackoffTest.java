package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.time.Duration;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class OutageBackoffTest {

    @Test
    void thePauseDoublesFromTheFirstAndStopsAtTheCap() {
        var wait = new OutageBackoff(Duration.ofSeconds(30), Duration.ofMillis(1), Duration.ofMillis(4)).start();

        assertEquals(Duration.ofMillis(1), wait.pause());
        assertTrue(wait.next());
        assertEquals(Duration.ofMillis(2), wait.pause());
        assertTrue(wait.next());
        assertEquals(Duration.ofMillis(4), wait.pause());
        assertTrue(wait.next());
        assertEquals(Duration.ofMillis(4), wait.pause(), "capped");
    }

    @Test
    void theStandardBoundIsShorterThanTheStreamsTwoMinuteDuplicateWindow() {
        assertTrue(OutageBackoff.standard().bound().compareTo(Duration.ofMinutes(2)) < 0);
        assertEquals(Duration.ofMillis(250), OutageBackoff.standard().start().pause());
    }

    @Test
    void whenThePauseWouldPassTheDeadlineItReturnsFalseWithoutSleeping() {
        var wait = new OutageBackoff(Duration.ofSeconds(1), Duration.ofSeconds(10)).start();
        long started = System.nanoTime();

        assertFalse(wait.next());

        assertTrue(Duration.ofNanos(System.nanoTime() - started).toMillis() < 500, "it did not sleep the pause");
    }

    @Test
    void resetPauseStartsTheDoublingOver() {
        var wait = new OutageBackoff(Duration.ofSeconds(30), Duration.ofMillis(1), Duration.ofMillis(8)).start();
        wait.next();
        wait.next();
        assertEquals(Duration.ofMillis(4), wait.pause());

        wait.resetPause();

        assertEquals(Duration.ofMillis(1), wait.pause());
    }

    @Test
    void anInterruptReturnsFalseAndKeepsTheInterruptFlag() {
        var wait = new OutageBackoff(Duration.ofSeconds(30), Duration.ofMillis(50)).start();
        Thread.currentThread().interrupt();
        try {
            assertFalse(wait.next());
            assertTrue(Thread.currentThread().isInterrupted(), "the flag is restored for the caller");
        } finally {
            Thread.interrupted(); // leave the test thread clean
        }
    }

    @Test
    void theBriefWaitIsShorterThanTheCloseWait() {
        assertTrue(OutageBackoff.brief().bound().compareTo(OutageBackoff.standard().bound()) < 0);
    }
}
