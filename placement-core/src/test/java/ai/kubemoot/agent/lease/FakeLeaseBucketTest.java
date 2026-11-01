package ai.kubemoot.agent.lease;

import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.time.Duration;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The fake bucket the other modules' lease tests stand on has to behave like the real one. */
class FakeLeaseBucketTest {

    private static final long TTL = Duration.ofSeconds(15).toNanos();

    private final AtomicLong clock = new AtomicLong(1);
    private final FakeLeaseBucket bucket = new FakeLeaseBucket(clock::get, TTL);
    private final LeaseStore a = bucket.storeFor();
    private final LeaseStore b = bucket.storeFor();

    @Test
    void oneHolderPerKeyAndOtherKeysAreIndependent() throws Exception {
        long first = a.acquire("t1", "a");
        assertTrue(first > 0);
        assertEquals(LeaseStore.NOT_HELD, b.acquire("t1", "b"));
        assertTrue(b.acquire("t2", "b") > 0);
        assertEquals("a", bucket.holder("t1"));
        assertEquals("b", bucket.holder("t2"));
    }

    @Test
    void renewalNeedsTheLatestRevisionAndExtendsTheLease() throws Exception {
        long first = a.acquire("t1", "a");
        clock.addAndGet(TTL - 1);
        long second = a.renew("t1", "a", first);
        assertTrue(second > first);
        clock.addAndGet(TTL - 1);
        assertTrue(a.isHeld("t1"), "a renewed lease outlives the first TTL");
        assertEquals(LeaseStore.NOT_HELD, a.renew("t1", "a", first), "a stale revision loses");
    }

    @Test
    void anUnrenewedLeaseLapsesAndAnotherHolderTakesIt() throws Exception {
        long first = a.acquire("t1", "a");
        clock.addAndGet(TTL);
        assertFalse(b.isHeld("t1"));
        assertNull(bucket.holder("t1"));
        assertTrue(b.acquire("t1", "b") > 0);
        assertEquals(LeaseStore.NOT_HELD, a.renew("t1", "a", first), "the deposed holder cannot renew");
    }

    @Test
    void releaseFreesTheKeyOnlyForTheLatestRevision() throws Exception {
        long first = a.acquire("t1", "a");
        long second = a.renew("t1", "a", first);
        a.release("t1", first);
        assertTrue(a.isHeld("t1"), "a stale revision does not release");
        a.release("t1", second);
        assertFalse(a.isHeld("t1"));
        assertTrue(b.acquire("t1", "b") > 0);
    }

    @Test
    void anUnreachableBucketFailsEveryCall() {
        bucket.setUnreachable(true);
        assertThrows(IOException.class, () -> a.acquire("t1", "a"));
        assertThrows(IOException.class, () -> a.isHeld("t1"));
    }
}
