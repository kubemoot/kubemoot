package ai.kubemoot.placement;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** One writer at a time: election, renewal, self-fencing, handover, and a lease the store drops. */
class LeadershipTest {

    private static final Duration LEASE = Duration.ofSeconds(15);

    /** The leader bucket: a lease entry that the bucket drops when it is not renewed within the TTL. */
    static final class FakeLeaseBucket {
        private final AtomicLong nanos;
        private String holder;
        private long revision;
        private long writtenAt;
        private long sequence;
        boolean unreachable;
        /** How long a renewal takes to answer, counted from when the bucket writes it. */
        Duration renewTakes = Duration.ZERO;

        FakeLeaseBucket(AtomicLong nanos) {
            this.nanos = nanos;
        }

        private void expire() {
            if (holder != null && nanos.get() - writtenAt >= LEASE.toNanos()) {
                holder = null;
            }
        }

        synchronized String holder() {
            expire();
            return holder;
        }

        LeaseStore storeFor() {
            return new LeaseStore() {
                @Override
                public long acquire(String who) throws Exception {
                    synchronized (FakeLeaseBucket.this) {
                        check();
                        expire();
                        if (holder != null) {
                            return NOT_HELD;
                        }
                        return write(who);
                    }
                }

                @Override
                public long renew(String who, long expected) throws Exception {
                    synchronized (FakeLeaseBucket.this) {
                        check();
                        expire();
                        if (holder == null || revision != expected) {
                            return NOT_HELD;
                        }
                        long written = write(who);
                        nanos.addAndGet(renewTakes.toNanos());
                        return written;
                    }
                }

                @Override
                public void release(long expected) throws Exception {
                    synchronized (FakeLeaseBucket.this) {
                        check();
                        if (holder != null && revision == expected) {
                            holder = null;
                        }
                    }
                }
            };
        }

        private void check() throws Exception {
            if (unreachable) {
                throw new java.io.IOException("NATS unreachable");
            }
        }

        private long write(String who) {
            holder = who;
            revision = ++sequence;
            writtenAt = nanos.get();
            return revision;
        }
    }

    /** Records what the service was told. */
    static class Recorder implements Leadership.Callbacks {
        final List<String> events = new ArrayList<>();
        boolean failElection;
        /** How much the clock moves while the previous leader's state is recovered. */
        Duration recoveryTakes = Duration.ZERO;
        AtomicLong clock;

        @Override
        public void elected() throws Exception {
            if (failElection) {
                throw new IllegalStateException("recovery failed");
            }
            if (clock != null) {
                clock.addAndGet(recoveryTakes.toNanos());
            }
            events.add("elected");
        }

        @Override
        public void demoted(boolean leaseLost) {
            events.add(leaseLost ? "demoted:lost" : "demoted:clean");
        }
    }

    private final AtomicLong nanos = new AtomicLong(1);
    private FakeLeaseBucket bucket;

    @BeforeEach
    void setUp() {
        bucket = new FakeLeaseBucket(nanos);
    }

    private Leadership replica(String name, Recorder recorder) {
        var l = new Leadership(ServiceSettings.forTest(name, LEASE, Duration.ofMinutes(10)),
                bucket::storeFor, nanos::get);
        l.register(recorder);
        return l;
    }

    private void advance(Duration d) {
        nanos.addAndGet(d.toNanos());
    }

    @Test
    void theFirstReplicaToAskBecomesTheLeader_andTheSecondDoesNot() {
        var a = replica("a", new Recorder());
        var b = replica("b", new Recorder());
        a.tick();
        b.tick();
        assertTrue(a.isLeader());
        assertFalse(b.isLeader());
        assertEquals("a", bucket.holder());
    }

    @Test
    void aLeaderThatKeepsRenewingKeepsTheLease_andAFollowerKeepsWaiting() {
        var a = replica("a", new Recorder());
        var b = replica("b", new Recorder());
        a.tick();
        for (int i = 0; i < 10; i++) {
            advance(LEASE.dividedBy(3));
            a.tick();
            b.tick();
        }
        assertTrue(a.isLeader());
        assertFalse(b.isLeader());
    }

    @Test
    void aCrashedLeaderIsReplacedOnceItsLeaseLapses_andNeverAnsweredAfterThat() {
        var ra = new Recorder();
        var a = replica("a", ra);
        var b = replica("b", new Recorder());
        a.tick();
        assertTrue(a.isLeader());

        // a stops renewing (crash or partition). Its own fence ends it before the lease can lapse.
        advance(LEASE.multipliedBy(2).dividedBy(3).plusMillis(1));
        assertFalse(a.isLeader(), "the fence is shorter than the lease");
        b.tick();
        assertFalse(b.isLeader(), "the lease has not lapsed yet");

        advance(LEASE.dividedBy(3));
        b.tick();
        assertTrue(b.isLeader());
        assertFalse(a.isLeader());
    }

    @Test
    void twoReplicasAreNeverLeaderAtTheSameTime() {
        var a = replica("a", new Recorder());
        var b = replica("b", new Recorder());
        a.tick();
        for (int i = 0; i < 40; i++) {
            advance(Duration.ofSeconds(1));
            if (i < 12) {
                a.tick();          // a renews for 12 seconds, then goes silent
            }
            b.tick();
            assertFalse(a.isLeader() && b.isLeader(), "two leaders at second " + i);
        }
        assertTrue(b.isLeader());
    }

    @Test
    void aLeaderThatCannotReachNatsStepsDownAtTheFence() {
        var r = new Recorder();
        var a = replica("a", r);
        a.tick();
        bucket.unreachable = true;
        advance(LEASE.dividedBy(3));
        a.tick();
        assertTrue(a.isLeader(), "one missed renewal is not yet the fence");
        advance(LEASE.dividedBy(3));
        a.tick();
        assertFalse(a.isLeader());
        assertEquals(List.of("elected", "demoted:lost"), r.events);
    }

    @Test
    void aLeaderThatFindsAnotherHolderStepsDown() {
        var r = new Recorder();
        var a = replica("a", r);
        a.tick();
        // The lease lapsed during a long pause and b took it; a wakes up and tries to renew.
        advance(LEASE.plusSeconds(1));
        var b = replica("b", new Recorder());
        b.tick();
        a.tick();
        assertFalse(a.isLeader());
        assertTrue(b.isLeader());
        assertTrue(r.events.contains("demoted:lost"));
    }

    @Test
    void aLeaderThatShutsDownHandsTheLeaseOverAtOnce() {
        var a = replica("a", new Recorder());
        var b = replica("b", new Recorder());
        a.tick();
        a.shutdown();
        assertFalse(a.isLeader());
        assertEquals(null, bucket.holder());
        b.tick();
        assertTrue(b.isLeader(), "b does not wait for a lease to lapse");
    }

    @Test
    void aReplicaWhoseRecoveryFailsGivesTheLeaseBack() {
        var failing = new Recorder();
        failing.failElection = true;
        var a = replica("a", failing);
        var b = replica("b", new Recorder());
        a.tick();
        assertFalse(a.isLeader());
        assertEquals(null, bucket.holder(), "a does not sit on a lease it cannot serve");
        b.tick();
        assertTrue(b.isLeader());
    }

    @Test
    void aCleanShutdownIsToldTheLeaseIsStillHeld_soTheMirrorCanDrain() {
        var r = new Recorder();
        var a = replica("a", r);
        a.tick();
        a.shutdown();
        assertEquals(List.of("elected", "demoted:clean"), r.events);
    }

    @Test
    void aRecoveryThatOutlastsTheFenceIsNotPromoted_andTheLeaseIsGivenBack() {
        var slow = new Recorder();
        slow.clock = nanos;
        slow.recoveryTakes = LEASE.multipliedBy(2).dividedBy(3).plusSeconds(1);
        var a = replica("a", slow);
        var b = replica("b", new Recorder());
        a.tick();
        assertFalse(a.isLeader(), "the lease was not renewed during recovery, so another replica may hold it");
        assertEquals(null, bucket.holder());
        assertEquals(List.of("elected", "demoted:lost"), slow.events);
        b.tick();
        assertTrue(b.isLeader());
    }

    @Test
    void aRenewalThatIsSlowToAnswerShortensTheFenceInsteadOfExtendingIt() {
        var a = replica("a", new Recorder());
        a.tick();
        advance(LEASE.dividedBy(3));
        // The renewal is sent now but the answer takes 6 seconds; the bucket's clock started when it was sent.
        bucket.renewTakes = Duration.ofSeconds(6);
        a.tick();
        advance(Duration.ofSeconds(4).plusMillis(1));
        assertFalse(a.isLeader(), "10 seconds after the renewal was sent the fence has passed, though only 4 seconds after the answer");
    }

    @Test
    void aReplicaIsOnlyLeaderOnceItHasRecovered() {
        var seenLeader = new boolean[1];
        var holder = new Leadership[1];
        var r = new Recorder() {
            @Override
            public void elected() throws Exception {
                seenLeader[0] = holder[0].isLeader();
                super.elected();
            }
        };
        holder[0] = replica("a", r);
        holder[0].tick();
        assertFalse(seenLeader[0], "requests are not answered while the previous leader's state is being recovered");
        assertTrue(holder[0].isLeader());
    }
}
