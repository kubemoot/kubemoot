package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** {@link TicketManager} over a {@link TicketStore} other than the NATS bucket. */
class TicketManagerStoreTest {

    private static final String SERVER = "rig0";

    private final FakeTicketStore store = new FakeTicketStore();
    private final TicketManager tickets = new TicketManager(store, new ObjectMapper());

    @Test
    void aClaimRunsAsOneExclusiveStep_andHoldsATicket() {
        var t = tickets.claim(SERVER, 8_000, 16_000);
        assertTrue(t.isPresent());
        assertEquals(1, store.exclusiveRuns.get());
        assertEquals(1, tickets.activeCountFor(SERVER));
        assertEquals(8_000, tickets.activeFootprintFor(SERVER));
    }

    @Test
    void releaseGivesTheBudgetBack_andReleasingTwiceIsHarmless() {
        var t = tickets.claim(SERVER, 8_000, 16_000).orElseThrow();
        tickets.release(t);
        tickets.release(t);
        assertEquals(0, tickets.activeCountFor(SERVER));
        assertTrue(store.keys().isEmpty());
    }

    @Test
    void aClaimThatDoesNotFitLeavesNothingBehind() {
        tickets.claim(SERVER, 10_000, 16_000).orElseThrow();
        assertTrue(tickets.claim(SERVER, 10_000, 0, 16_000, "other-model").isEmpty());
        assertEquals(1, store.keys().size());
    }

    @Test
    void anUnavailableStoreRefusesTheClaimWithoutThrowing() {
        store.setAvailable(false);
        assertTrue(tickets.claim(SERVER, 8_000, 16_000).isEmpty());
        tickets.release(null);
        assertEquals(0, tickets.activeCountFor(SERVER));
    }

    @Test
    void earlierClaimsAreAheadOfLaterOnes() {
        var first = tickets.claimSlot(SERVER, 1_000, 0, 16_000, "m", ahead -> ahead < 1);
        var second = tickets.claimSlot(SERVER, 1_000, 0, 16_000, "m", ahead -> ahead < 1);
        assertTrue(first.isPresent(), "the first claim has no one ahead");
        assertTrue(second.isEmpty(), "the second sees the first ahead and gives its ticket back");
        assertEquals(1, tickets.activeCountFor(SERVER));
    }

    @Test
    void aSecondLoadOfTheSameModelJoinsTheFirst() {
        var first = tickets.claimLoad(SERVER, 8_000, 0, 16_000, "m", Set.of(SERVER));
        var second = tickets.claimLoad(SERVER, 8_000, 0, 16_000, "m", Set.of(SERVER));
        assertTrue(first.isPresent());
        assertTrue(second.isEmpty());
    }

    @Test
    void residencyIsKeptApartFromTickets() {
        tickets.recordResidency(SERVER, "qwen3:14b", 9_000);
        assertEquals(Map.of("qwen3:14b", 9_000L), tickets.residentFootprintsFor(SERVER));
        assertEquals(0, tickets.activeCountFor(SERVER), "a residency entry is not a call in flight");
        tickets.clearResidency(SERVER, "qwen3:14b");
        assertTrue(tickets.residentFootprintsFor(SERVER).isEmpty());
    }

    @Test
    void activeModelsAndKvCacheAreReadFromTheTickets() {
        tickets.claim(SERVER, 4_000, 512, 16_000, "m1");
        assertEquals(Set.of("m1"), tickets.activeModelsOn(SERVER));
        assertEquals(512, tickets.activeKvCacheFor(SERVER));
        assertFalse(tickets.inFlightStartsFor(SERVER).isEmpty());
    }
}
