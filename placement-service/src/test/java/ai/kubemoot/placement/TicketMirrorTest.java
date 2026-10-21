package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.TicketManager;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.concurrent.TimeoutException;
import java.util.function.BooleanSupplier;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The NATS mirror: write-behind, recovery after a restart, foreign writers, lost writes, and markers. */
class TicketMirrorTest {

    private static final ObjectMapper M = new ObjectMapper();
    private static final long TTL_MS = 600_000;

    private FakeMirrorBucket bucket;
    private TicketTable table;
    private TicketMirror mirror;

    @BeforeEach
    void setUp() {
        bucket = new FakeMirrorBucket();
        table = newTable();
        mirror = newMirror(table);
    }

    @AfterEach
    void tearDown() {
        mirror.stop(Duration.ZERO);
    }

    private static TicketTable newTable() {
        return new TicketTable(TTL_MS, System::currentTimeMillis);
    }

    private TicketMirror newMirror(TicketTable t) {
        var m = new TicketMirror(t, () -> bucket, M, TTL_MS * 2, System::currentTimeMillis);
        t.addListener(m);
        return m;
    }

    private static ObjectNode doc(String ticketId, String model) {
        ObjectNode n = M.createObjectNode();
        n.put("ticketId", ticketId);
        n.put("modelName", model);
        return n;
    }

    private static byte[] bytes(ObjectNode n) throws Exception {
        return M.writeValueAsBytes(n);
    }

    private static void eventually(BooleanSupplier condition) throws InterruptedException {
        long until = System.currentTimeMillis() + 5_000;
        while (!condition.getAsBoolean() && System.currentTimeMillis() < until) {
            Thread.sleep(10);
        }
        assertTrue(condition.getAsBoolean(), "condition not met within 5s");
    }

    private void start() throws Exception {
        mirror.start(Duration.ofSeconds(2));
        table.activate();
    }

    // ---- write-behind ----

    @Test
    void aClaimIsMirroredToTheBucket_andItsReleaseRemovesIt() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();

        eventually(() -> bucket.entries.containsKey(t.keyName()));
        tickets.release(t);
        eventually(() -> !bucket.entries.containsKey(t.keyName()));
        assertEquals(1, bucket.removedKeys.size());
    }

    @Test
    void theMirroredDocumentIsTheTicketSchemaOtherReadersExpect() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 256, 16_000, "qwen3:14b").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));

        var stored = M.readTree(bucket.entries.get(t.keyName()));
        assertEquals(t.ticketId(), stored.path("ticketId").asText());
        assertEquals("rig0", stored.path("server").asText());
        assertEquals(5_000, stored.path("modelFootprintMiB").asLong());
        assertEquals(256, stored.path("kvCacheFootprintMiB").asLong());
        assertEquals("qwen3:14b", stored.path("modelName").asText());
    }

    @Test
    void theOwnEchoOfAWriteIsNotTakenForAForeignOne() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));
        tickets.release(t);
        eventually(() -> !bucket.entries.containsKey(t.keyName()));

        assertEquals(0, tickets.activeCountFor("rig0"), "a released ticket is not brought back by its own echo");
    }

    // ---- recovery ----

    @Test
    void aNewLeaderRecoversTheTicketsThePreviousLeaderLeft() throws Exception {
        start();
        var first = new TicketManager(table, M);
        var kept = first.claim("rig0", 5_000, 0, 16_000, "m1").orElseThrow();
        var gone = first.claim("rig0", 2_000, 0, 16_000, "m2").orElseThrow();
        first.release(gone);
        first.recordResidency("rig0", "m1", 5_000);
        eventually(() -> bucket.entries.containsKey(kept.keyName()) && !bucket.entries.containsKey(gone.keyName())
                && bucket.entries.size() == 2);
        mirror.stop(Duration.ofSeconds(1));

        // The next replica starts with an empty table and follows the same bucket.
        TicketTable fresh = newTable();
        TicketMirror next = newMirror(fresh);
        try {
            next.start(Duration.ofSeconds(2));
            fresh.activate();
            var recovered = new TicketManager(fresh, M);
            assertEquals(1, recovered.activeCountFor("rig0"));
            assertEquals(5_000, recovered.activeFootprintFor("rig0"));
            assertEquals(java.util.Map.of("m1", 5_000L), recovered.residentFootprintsFor("rig0"));
            assertTrue(fresh.deleteTicket(kept.ticketId()), "a recovered ticket can still be released by its id");
        } finally {
            next.stop(Duration.ZERO);
        }
    }

    @Test
    void recoveryIgnoresRemovalMarkersAndEntriesPastTheirTtl() throws Exception {
        long now = System.currentTimeMillis();
        bucket.seed("rig0__fresh", bytes(doc("fresh", "m")));
        bucket.seed("rig0__old", bytes(doc("old", "m")), now - TTL_MS - 1_000);
        bucket.seed("rig0__unreadable", "{not json".getBytes(StandardCharsets.UTF_8));
        start();
        bucket.foreignWrite("rig0__marker", null, now);
        assertEquals(1, new TicketManager(table, M).activeCountFor("rig0"));
        assertTrue(table.holds("rig0__fresh"));
        assertFalse(table.holds("rig0__old"), "the previous leader's ticket outlived its TTL");
        assertFalse(table.holds("rig0__marker"));
    }

    @Test
    void startFailsWhenTheBucketNeverDeliversItsEntries() {
        bucket.neverCatchUp();
        assertThrows(TimeoutException.class, () -> mirror.start(Duration.ofMillis(100)));
        assertFalse(mirror.running());
        assertEquals(0, bucket.watching(), "the watch is closed again");
    }

    // ---- foreign writers ----

    @Test
    void aTicketAnotherWriterPutsInTheBucketCountsAgainstTheBudget() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        ObjectNode probe = doc("probe-1", "calibration");
        probe.put("server", "rig0");
        probe.put("modelFootprintMiB", 12_000);
        bucket.foreignWrite("rig0__probe-1", bytes(probe), System.currentTimeMillis());

        assertEquals(12_000, tickets.activeFootprintFor("rig0"));
        assertTrue(tickets.claim("rig0", 6_000, 0, 16_000, "m").isEmpty(), "the probe's 12000 leaves no room for 6000");

        bucket.foreignWrite("rig0__probe-1", null, System.currentTimeMillis());
        assertEquals(0, tickets.activeFootprintFor("rig0"));
        assertTrue(tickets.claim("rig0", 6_000, 0, 16_000, "m").isPresent());
    }

    @Test
    void aForeignRemovalOfAResidencyEntryIsIgnored() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        tickets.recordResidency("rig0", "m", 5_000);
        eventually(() -> !bucket.entries.isEmpty());
        bucket.foreignWrite("residency__rig0__m", null, System.currentTimeMillis());
        assertEquals(java.util.Map.of("m", 5_000L), tickets.residentFootprintsFor("rig0"));
    }

    @Test
    void aForeignRemovalOfATicketThisReplicaClaimedChangesNothing() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));
        bucket.foreignWrite(t.keyName(), null, System.currentTimeMillis());
        assertEquals(1, tickets.activeCountFor("rig0"), "the table is the authority for tickets it handed out");
    }

    // ---- lost writes and markers ----

    @Test
    void aWriteThatFailsEveryAttemptIsRewrittenFromTheTable() throws Exception {
        start();
        bucket.failNextWrites(3);
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> mirror.failed() == 1);
        assertFalse(bucket.entries.containsKey(t.keyName()));

        mirror.tidyBucket();
        eventually(() -> bucket.entries.containsKey(t.keyName()));
        assertTrue(bucket.markerSweeps.get() >= 1, "removal markers are swept on the same pass");
    }

    @Test
    void aReleaseThatWasLostIsRepaired_soTheNextLeaderDoesNotRecoverAReleasedTicket() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));

        bucket.failNextWrites(3);
        tickets.release(t);
        eventually(() -> mirror.failed() == 1);
        assertTrue(bucket.entries.containsKey(t.keyName()), "the removal never reached the bucket");

        mirror.tidyBucket();
        eventually(() -> !bucket.entries.containsKey(t.keyName()));
    }

    @Test
    void whenTheQueueOverflowsOnARemovalTheRepairStillRemovesIt() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));
        tickets.release(t);
        eventually(() -> !bucket.entries.containsKey(t.keyName()));

        // A key the bucket holds and the table does not, as after a dropped removal.
        bucket.foreignWrite("rig0__stale", bytes(doc("stale", "m")), 0);
        mirror.resync();
        eventually(() -> !bucket.entries.containsKey("rig0__stale"));
    }

    @Test
    void aMirrorWhoseLeaseIsGoneWritesNothing() throws Exception {
        boolean[] held = {true};
        mirror.fenceWith(() -> held[0]);
        start();
        var tickets = new TicketManager(table, M);
        held[0] = false;
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m");
        Thread.sleep(700);
        assertTrue(bucket.entries.isEmpty(), "a replica that lost the lease must not write behind the new leader");
        assertEquals(0, mirror.written());
    }

    @Test
    void stoppingWithoutADrainDropsWhatIsQueued() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        bucket.failNextWrites(100);
        tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        mirror.stop(Duration.ZERO);
        assertEquals(0, mirror.queued());
        assertTrue(mirror.bucketKeys().isEmpty());
    }

    @Test
    void aTransientWriteFailureIsRetried() throws Exception {
        start();
        bucket.failNextWrites(1);
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        eventually(() -> bucket.entries.containsKey(t.keyName()));
        assertEquals(0, mirror.failed());
    }

    @Test
    void expiredTicketsAreDroppedAndTheirRemovalIsMirrored() throws Exception {
        long[] clock = {System.currentTimeMillis()};
        TicketTable shortLived = new TicketTable(1_000, () -> clock[0]);
        var m = new TicketMirror(shortLived, () -> bucket, M, 2_000, () -> clock[0]);
        shortLived.addListener(m);
        try {
            m.start(Duration.ofSeconds(2));
            shortLived.activate();
            var tickets = new TicketManager(shortLived, M);
            var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
            eventually(() -> bucket.entries.containsKey(t.keyName()));

            clock[0] += 1_500;
            m.expire();
            eventually(() -> !bucket.entries.containsKey(t.keyName()));
        } finally {
            m.stop(Duration.ZERO);
        }
    }

    @Test
    void stoppingWritesWhatIsQueuedFirst() throws Exception {
        start();
        var tickets = new TicketManager(table, M);
        var t = tickets.claim("rig0", 5_000, 0, 16_000, "m").orElseThrow();
        mirror.stop(Duration.ofSeconds(2));
        assertTrue(bucket.entries.containsKey(t.keyName()), "a handover loses no claim");
        assertEquals(0, bucket.watching());
    }
}
