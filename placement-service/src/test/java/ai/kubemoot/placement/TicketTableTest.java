package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.Ticket;
import ai.kubemoot.agent.modelserver.TicketManager;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The in-memory ticket table: the single writer's claims, expiry, and the entries the mirror brings in. */
class TicketTableTest {

    private static final ObjectMapper M = new ObjectMapper();
    private static final long TTL_MS = 60_000;

    private final AtomicLong now = new AtomicLong(1_000_000);
    private TicketTable table;
    private final List<TicketTable.Change> changes = new ArrayList<>();

    @BeforeEach
    void setUp() {
        table = new TicketTable(TTL_MS, now::get);
        table.activate();
        table.addListener(changes::add);
    }

    private static ObjectNode doc(String ticketId, String model) {
        ObjectNode n = M.createObjectNode();
        n.put("ticketId", ticketId);
        n.put("modelName", model);
        return n;
    }

    @Test
    void creatingAKeyTwiceFails_andRevisionsOrderTheClaims() {
        long first = table.create("rig0__a", doc("a", "m"));
        long second = table.create("rig0__b", doc("b", "m"));
        assertTrue(second > first);
        assertThrows(IllegalStateException.class, () -> table.create("rig0__a", doc("a", "m")));
    }

    @Test
    void scanReturnsOnlyTheKeysUnderThePrefix_inClaimOrderByKey() {
        table.create("rig0__a", doc("a", "m"));
        table.create("rig0__b", doc("b", "m"));
        table.create("rig1__c", doc("c", "m"));
        assertEquals(2, table.scan("rig0__").size());
        assertEquals(1, table.scan("rig1__").size());
        assertTrue(table.scan("rig2__").isEmpty());
        assertEquals(3, table.snapshot().size());
    }

    @Test
    void aTicketIsReleasedByItsId_andReleasingTwiceIsHarmless() {
        table.create("rig0__a", doc("a", "m"));
        assertTrue(table.deleteTicket("a"));
        assertFalse(table.deleteTicket("a"));
        assertFalse(table.deleteTicket("never-existed"));
        assertTrue(table.scan("rig0__").isEmpty());
    }

    @Test
    void anEntryNobodyReleasedExpires_andIsSweptAndPublished() {
        table.create("rig0__a", doc("a", "m"));
        now.addAndGet(TTL_MS - 1);
        assertEquals(1, table.scan("rig0__").size());
        now.addAndGet(2);
        assertTrue(table.scan("rig0__").isEmpty(), "an expired entry is no longer counted");
        changes.clear();
        assertEquals(List.of("rig0__a"), table.sweep());
        assertTrue(changes.get(0).removal());
        assertEquals(0, table.size());
    }

    @Test
    void aResidencyEntryLivesAsLongAsItIsRefreshed() {
        table.put("residency__rig0__m", doc("x", "m"));
        now.addAndGet(TTL_MS - 1);
        table.put("residency__rig0__m", doc("x", "m"));
        now.addAndGet(TTL_MS - 1);
        assertEquals(1, table.scan("residency__rig0__").size());
    }

    @Test
    void anInactiveTableRefusesClaims_andForgetsOnDeactivate() {
        table.create("rig0__a", doc("a", "m"));
        table.deactivate();
        assertFalse(table.available());
        assertThrows(IllegalStateException.class, () -> table.create("rig0__b", doc("b", "m")));
        table.activate();
        assertEquals(0, table.size(), "a replica that stopped leading starts over from the mirror");
    }

    @Test
    void ingestKeepsWhatTheTableAlreadyHolds_andSkipsWhatAlreadyExpired() {
        table.create("rig0__a", doc("a", "mine"));
        assertFalse(table.ingest("rig0__a", doc("a", "theirs"), now.get()));
        assertEquals("mine", table.scan("rig0__").get(0).doc().path("modelName").asText());

        assertTrue(table.ingest("rig0__b", doc("b", "recovered"), now.get() - 1_000));
        assertFalse(table.ingest("rig0__c", doc("c", "stale"), now.get() - TTL_MS - 1));
        assertEquals(2, table.scan("rig0__").size());
    }

    @Test
    void anIngestedEntryExpiresFromWhenItWasWritten() {
        table.ingest("rig0__b", doc("b", "recovered"), now.get() - TTL_MS + 5_000);
        now.addAndGet(5_001);
        assertTrue(table.scan("rig0__").isEmpty());
    }

    @Test
    void listenersSeeLocalAndForeignChangesApart() {
        table.create("rig0__a", doc("a", "m"));
        table.ingest("rig0__b", doc("b", "m"), now.get());
        table.removeForeign("rig0__b");
        table.delete("rig0__a");
        assertEquals(List.of(TicketTable.Source.LOCAL, TicketTable.Source.FOREIGN, TicketTable.Source.FOREIGN,
                TicketTable.Source.LOCAL), changes.stream().map(TicketTable.Change::source).toList());
        assertEquals(List.of(false, false, true, true), changes.stream().map(TicketTable.Change::removal).toList());
    }

    @Test
    void removingAnAbsentKeyPublishesNothing() {
        table.delete("rig0__nope");
        assertFalse(table.removeForeign("rig0__nope"));
        assertTrue(changes.isEmpty());
    }

    @Test
    void aTableWhoseLeaseIsGoneRefusesClaimsAtOnce_beforeItIsDeactivated() {
        table.fenceWith(() -> false);
        assertFalse(table.available(), "the fence applies the moment the lease passes, whether or not the replica noticed");
        assertThrows(IllegalStateException.class, () -> table.create("rig0__a", doc("a", "m")));
        table.put("residency__rig0__m", doc("x", "m"));
        assertEquals(0, table.size());
    }

    @Test
    void listenersSeeEachKeysChangesInTheOrderTheyHappened() throws Exception {
        List<String> seen = java.util.Collections.synchronizedList(new ArrayList<>());
        table.addListener(c -> seen.add(c.key() + (c.removal() ? ":remove" : ":put")));
        ExecutorService pool = Executors.newFixedThreadPool(8);
        try {
            List<Future<?>> work = new ArrayList<>();
            for (int i = 0; i < 200; i++) {
                String id = "k" + i;
                work.add(pool.submit(() -> {
                    table.create("rig0__" + id, doc(id, "m"));
                    table.deleteTicket(id);
                }));
            }
            for (Future<?> f : work) {
                f.get(10, TimeUnit.SECONDS);
            }
        } finally {
            pool.shutdownNow();
        }
        for (int i = 0; i < 200; i++) {
            int put = seen.indexOf("rig0__k" + i + ":put");
            int remove = seen.indexOf("rig0__k" + i + ":remove");
            assertTrue(put >= 0 && remove > put, "a release overtook its claim for k" + i);
        }
    }

    @Test
    void ticketManagerClaimsAreAtomic_soNoTwoClaimsFitWhatFitsOne() throws Exception {
        var tickets = new TicketManager(table, M);
        ExecutorService pool = Executors.newFixedThreadPool(8);
        try {
            List<Future<Boolean>> claims = new ArrayList<>();
            for (int i = 0; i < 40; i++) {
                claims.add(pool.submit(() -> tickets.claim("rig0", 6_000, 0, 16_000, "m" + Thread.currentThread()
                        .threadId() + System.nanoTime()).isPresent()));
            }
            int granted = 0;
            for (Future<Boolean> f : claims) {
                granted += f.get(10, TimeUnit.SECONDS) ? 1 : 0;
            }
            assertEquals(2, granted, "16000 MiB of headroom holds two 6000 MiB claims, however many race");
            assertEquals(2, tickets.activeCountFor("rig0"));
        } finally {
            pool.shutdownNow();
        }
    }

    @Test
    void aClaimThroughTheManagerCanBeReleasedByTheIdThatTheCallerHolds() {
        var tickets = new TicketManager(table, M);
        Ticket t = tickets.claim("rig0", 1_000, 0, 16_000, "m").orElseThrow();
        assertTrue(table.deleteTicket(t.ticketId()));
        assertEquals(0, tickets.activeCountFor("rig0"));
    }
}
