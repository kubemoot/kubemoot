package ai.kubemoot.placement;

import org.junit.jupiter.api.Test;

import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class LeaseFenceTest {

    @Test
    void theLeaseCountsAsHeldUntilAFenceIsSet() {
        assertTrue(new LeaseFence().held());
    }

    @Test
    void theFenceDecidesEachTimeItIsAsked() {
        LeaseFence fence = new LeaseFence();
        AtomicBoolean leader = new AtomicBoolean(true);
        fence.set(leader::get);

        assertTrue(fence.held());
        leader.set(false);
        assertFalse(fence.held(), "a deposed leader passes the fence the moment it loses the lease");
        leader.set(true);
        assertTrue(fence.held());
    }

    @Test
    void aLaterFenceReplacesTheEarlierOne() {
        LeaseFence fence = new LeaseFence();
        fence.set(() -> false);
        fence.set(() -> true);

        assertTrue(fence.held());
    }
}
