package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SubscribeRetryTest {

    private static final Duration QUICK = Duration.ofMillis(1);
    private static final long SAFETY_SECONDS = 10;

    private SubscribeRetry retry;

    @AfterEach
    void stopRetry() {
        if (retry != null) {
            retry.stop();
        }
    }

    @Test
    void aFirstAttemptThatSucceedsSchedulesNothing() {
        retry = new SubscribeRetry("test", QUICK, QUICK);
        var attempts = new AtomicInteger();
        assertTrue(retry.attemptOrRetry(() -> {
            attempts.incrementAndGet();
            return true;
        }));
        assertEquals(1, attempts.get());
    }

    @Test
    void retriesUntilTheAttemptSucceedsAndThenNeverRunsItAgain() throws Exception {
        retry = new SubscribeRetry("test", QUICK, QUICK);
        var attempts = new AtomicInteger();
        var done = new CountDownLatch(1);
        assertFalse(retry.attemptOrRetry(() -> {
            if (attempts.incrementAndGet() < 4) {
                return false;
            }
            done.countDown();
            return true;
        }));
        assertTrue(done.await(SAFETY_SECONDS, TimeUnit.SECONDS), "never subscribed");
        assertTrue(retry.isTerminatedWithinForTest(Duration.ofSeconds(SAFETY_SECONDS)),
                "the retry thread kept running after the success");
        assertEquals(4, attempts.get());
    }

    @Test
    void anAttemptThatThrowsDoesNotEndTheRetries() throws Exception {
        retry = new SubscribeRetry("test", QUICK, QUICK);
        var attempts = new AtomicInteger();
        var done = new CountDownLatch(1);
        retry.attemptOrRetry(() -> {
            if (attempts.incrementAndGet() < 3) {
                throw new IllegalStateException("connection closed");
            }
            done.countDown();
            return true;
        });
        assertTrue(done.await(SAFETY_SECONDS, TimeUnit.SECONDS), "a thrown attempt ended the retries");
    }

    @Test
    void stopEndsTheRetries() throws Exception {
        retry = new SubscribeRetry("test", QUICK, QUICK);
        var attempts = new AtomicInteger();
        var second = new CountDownLatch(2);
        retry.attemptOrRetry(() -> {
            attempts.incrementAndGet();
            second.countDown();
            return false;
        });
        assertTrue(second.await(SAFETY_SECONDS, TimeUnit.SECONDS));
        retry.stop();
        assertTrue(retry.isTerminatedWithinForTest(Duration.ofSeconds(SAFETY_SECONDS)));
        int atEnd = attempts.get();
        retry.stop(); // a second stop is harmless
        assertEquals(atEnd, attempts.get(), "an attempt ran after the retry thread ended");
    }

    @Test
    void nothingIsAttemptedAfterStop() {
        retry = new SubscribeRetry("test", QUICK, QUICK);
        retry.stop();
        var attempts = new AtomicInteger();
        assertFalse(retry.attemptOrRetry(() -> {
            attempts.incrementAndGet();
            return true;
        }));
        assertEquals(0, attempts.get());
    }

    @Test
    void theWaitDoublesAndStopsAtTheMaximum() {
        retry = new SubscribeRetry("test", Duration.ofSeconds(1), Duration.ofSeconds(5));
        assertEquals(Duration.ofSeconds(1), retry.first());
        assertEquals(Duration.ofSeconds(2), retry.nextDelay(Duration.ofSeconds(1)));
        assertEquals(Duration.ofSeconds(4), retry.nextDelay(Duration.ofSeconds(2)));
        assertEquals(Duration.ofSeconds(5), retry.nextDelay(Duration.ofSeconds(4)));
        assertEquals(Duration.ofSeconds(5), retry.nextDelay(Duration.ofSeconds(5)));
    }

    @Test
    void aZeroFirstWaitUsesTheDefaultInsteadOfSpinning() {
        retry = new SubscribeRetry("test", Duration.ZERO, Duration.ZERO);
        assertEquals(SubscribeRetry.FIRST, retry.first());
        assertEquals(SubscribeRetry.FIRST, retry.nextDelay(retry.first()), "a Max below First is raised to First");
    }
}
