package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class DiscussionRateLimiterTest {

    @Test
    void firstAcquire_succeeds() {
        var limiter = new DiscussionRateLimiter();
        assertTrue(limiter.tryAcquire("thread-1"));
    }

    @Test
    void withinThreadLimit_allSucceed() {
        var limiter = new DiscussionRateLimiter(10, 3);
        assertTrue(limiter.tryAcquire("thread-1"));
        assertTrue(limiter.tryAcquire("thread-1"));
        assertTrue(limiter.tryAcquire("thread-1"));
    }

    @Test
    void exceedsThreadLimit_returnsFalse() {
        var limiter = new DiscussionRateLimiter(10, 3);
        limiter.tryAcquire("thread-1");
        limiter.tryAcquire("thread-1");
        limiter.tryAcquire("thread-1");
        assertFalse(limiter.tryAcquire("thread-1"));
    }

    @Test
    void separateThreads_independent() {
        var limiter = new DiscussionRateLimiter(10, 2);
        assertTrue(limiter.tryAcquire("thread-1"));
        assertTrue(limiter.tryAcquire("thread-1"));
        assertFalse(limiter.tryAcquire("thread-1"));
        // thread-2 is independent
        assertTrue(limiter.tryAcquire("thread-2"));
        assertTrue(limiter.tryAcquire("thread-2"));
    }

    @Test
    void getThreadCount_afterAcquires() {
        var limiter = new DiscussionRateLimiter();
        assertEquals(0, limiter.getThreadCount("thread-1"));
        limiter.tryAcquire("thread-1");
        limiter.tryAcquire("thread-1");
        assertEquals(2, limiter.getThreadCount("thread-1"));
    }

    @Test
    void exceedDoesNotIncrementCount() {
        var limiter = new DiscussionRateLimiter(10, 2);
        limiter.tryAcquire("thread-1");
        limiter.tryAcquire("thread-1");
        assertFalse(limiter.tryAcquire("thread-1")); // fails
        assertEquals(2, limiter.getThreadCount("thread-1")); // still 2, not 3
    }

    @Test
    void zeroMaxPerThread_defaultsToThree() {
        var limiter = new DiscussionRateLimiter(10, 0);
        assertTrue(limiter.tryAcquire("t"));
        assertTrue(limiter.tryAcquire("t"));
        assertTrue(limiter.tryAcquire("t"));
        assertFalse(limiter.tryAcquire("t"));
    }

    @Test
    void zeroMaxPerMinute_noRateLimit() {
        var limiter = new DiscussionRateLimiter(0, 100);
        // With maxPerMinute=0 → Integer.MAX_VALUE, should not hit rate limit
        for (int i = 0; i < 100; i++) {
            assertTrue(limiter.tryAcquire("thread-" + i));
        }
    }
}
