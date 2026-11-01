package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.lease.FakeLeaseBucket;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** One owner per thread: taking, renewing, fencing, losing and giving up a lease. */
class CoordinatorLeasesTest {

    private static final long TTL = CoordinatorLeases.LEASE.toNanos();
    private static final String PREFIX = "ns.crew.";

    private final AtomicLong nanos = new AtomicLong(1);
    private final FakeLeaseBucket bucket = new FakeLeaseBucket(nanos::get, TTL);
    private final Set<String> open = new HashSet<>();
    private final List<String> lost = new ArrayList<>();

    private CoordinatorLeases replica(String pod) {
        return new CoordinatorLeases(bucket::storeFor, () -> PREFIX, pod, nanos::get, open::contains, lost::add);
    }

    private void advance(Duration time) {
        nanos.addAndGet(time.toNanos());
    }

    @Test
    void oneReplicaOwnsAThreadAtATime() {
        var a = replica("pod-a");
        var b = replica("pod-b");

        assertTrue(a.acquire("t1"));
        assertFalse(b.acquire("t1"), "the thread is taken");
        assertTrue(b.acquire("t2"), "another thread is free");
        assertEquals("pod-a", bucket.holder(PREFIX + "t1"));
        assertTrue(a.acquire("t1"), "holding it already is holding it");
    }

    @Test
    void theKeyNamesTheNamespaceAndCrewSoCrewsDoNotMeet() {
        var a = replica("pod-a");
        a.acquire("t1");

        assertEquals("pod-a", bucket.holder("ns.crew.t1"));
        assertNull(bucket.holder("t1"));
    }

    @Test
    void renewingKeepsTheLeaseWhileTheThreadIsOpen() {
        var a = replica("pod-a");
        open.add("t1");
        a.acquire("t1");

        for (int i = 0; i < 6; i++) {
            advance(CoordinatorLeases.RENEW_EVERY);
            a.tick();
        }

        assertTrue(a.holds("t1"));
        assertEquals("pod-a", bucket.holder(PREFIX + "t1"), "renewed past the first TTL");
        assertTrue(lost.isEmpty());
    }

    @Test
    void aReplicaThatStopsRenewingLosesTheThreadToAnotherAndLearnsItOnItsNextRenewal() {
        var a = replica("pod-a");
        var b = replica("pod-b");
        open.add("t1");
        a.acquire("t1");

        advance(CoordinatorLeases.LEASE);
        assertTrue(b.acquire("t1"), "the lease lapsed, so another replica takes the thread");
        a.tick();

        assertEquals(List.of("t1"), lost, "the deposed replica is told");
        assertFalse(a.holds("t1"));
        assertEquals("pod-b", bucket.holder(PREFIX + "t1"));
    }

    @Test
    void aHolderThatCannotRenewFencesItselfBeforeTheBucketHandsTheLeaseOver() {
        var a = replica("pod-a");
        open.add("t1");
        a.acquire("t1");
        bucket.setUnreachable(true);

        advance(Duration.ofSeconds(9));
        a.tick();
        assertTrue(a.holds("t1"), "inside the fence it still holds");
        advance(Duration.ofSeconds(2));
        assertFalse(a.holds("t1"), "past two thirds of the lease it acts as if it lost it");
        a.tick();

        assertEquals(List.of("t1"), lost);
    }

    @Test
    void aClosedThreadsLeaseIsGivenUpAtTheNextRenewal() {
        var a = replica("pod-a");
        var b = replica("pod-b");
        open.add("t1");
        a.acquire("t1");
        open.remove("t1");

        advance(CoordinatorLeases.RENEW_EVERY);
        a.tick();

        assertNull(bucket.holder(PREFIX + "t1"));
        assertTrue(b.acquire("t1"));
        assertTrue(lost.isEmpty(), "giving a lease up is not losing it");
    }

    @Test
    void thePumpLeaseIsKeptWhetherOrNotAThreadIsOpenForItsName() {
        var a = replica("pod-a");
        a.acquire(CoordinatorLeases.PUMP);

        advance(CoordinatorLeases.RENEW_EVERY);
        a.tick();

        assertTrue(a.holds(CoordinatorLeases.PUMP));
        assertFalse(replica("pod-b").acquire(CoordinatorLeases.PUMP));
    }

    @Test
    void releaseAllHandsEveryLeaseOverAtOnce() {
        var a = replica("pod-a");
        open.addAll(Set.of("t1", "t2"));
        a.acquire("t1");
        a.acquire("t2");
        a.acquire(CoordinatorLeases.PUMP);

        a.releaseAll();

        var b = replica("pod-b");
        assertTrue(b.acquire("t1"));
        assertTrue(b.acquire("t2"));
        assertTrue(b.acquire(CoordinatorLeases.PUMP));
    }

    @Test
    void anUnreachableBucketMeansTheLeaseIsNotTaken() {
        var a = replica("pod-a");
        bucket.setUnreachable(true);

        assertFalse(a.acquire("t1"));
        assertFalse(a.holds("t1"));
    }

    @Test
    void withLeasesOffEveryThreadIsOwnedByTheOneReplica() {
        var off = CoordinatorLeases.off();

        assertFalse(off.enabled());
        assertTrue(off.acquire("t1"));
        assertTrue(off.holds("t1"));
        off.tick();
        off.release("t1");
        off.releaseAll();
        assertTrue(off.holds("anything"));
    }

    @Test
    void aLeaseJustTakenIsRenewedEvenIfItsThreadIsNotRegisteredYet() {
        var a = replica("pod-a");
        a.acquire("t1");

        a.tick();

        assertEquals("pod-a", bucket.holder(PREFIX + "t1"), "the thread joins the map after the lease is won");
        advance(CoordinatorLeases.RENEW_EVERY);
        a.tick();
        assertNull(bucket.holder(PREFIX + "t1"), "but a thread that never registered is let go one period later");
    }

    @Test
    void aSlowRenewalDoesNotBlockTakingOrAskingAboutOtherLeases() throws Exception {
        var gate = new java.util.concurrent.CountDownLatch(1);
        var entered = new java.util.concurrent.CountDownLatch(1);
        var inner = bucket.storeFor();
        var slow = new ai.kubemoot.agent.lease.LeaseStore() {
            @Override
            public long acquire(String key, String holder) throws Exception {
                return inner.acquire(key, holder);
            }

            @Override
            public long renew(String key, String holder, long revision) throws Exception {
                entered.countDown();
                gate.await();
                return inner.renew(key, holder, revision);
            }

            @Override
            public void release(String key, long revision) throws Exception {
                inner.release(key, revision);
            }
        };
        var a = new CoordinatorLeases(() -> slow, () -> PREFIX, "pod-a", nanos::get, name -> true, lost::add);
        a.acquire("t1");
        advance(CoordinatorLeases.RENEW_EVERY);
        var ticking = new Thread(a::tick);
        ticking.start();
        assertTrue(entered.await(5, java.util.concurrent.TimeUnit.SECONDS));

        assertTrue(a.acquire("t2"), "taking another lease does not wait for the renewal");
        assertTrue(a.holds("t2"));
        assertTrue(a.holds("t1"));

        gate.countDown();
        ticking.join(5000);
        assertTrue(lost.isEmpty());
    }
}
