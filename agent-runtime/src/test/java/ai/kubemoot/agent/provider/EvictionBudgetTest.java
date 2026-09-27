package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.RepeatedTest;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CyclicBarrier;

import static org.junit.jupiter.api.Assertions.*;

/** Planned evictions ride on the ticket so two planners cannot spend the same freed memory. */
class EvictionBudgetTest {

    private static final String GPU = "ollama-5090";
    private static final long HEADROOM = 2_000;          // free now
    private static final long VICTIM_MIB = 14_000;       // idle qwen3:14b
    private static final long LOAD_MIB = 12_000;         // each planner wants to load 12 GiB

    @Test
    void creditCountsEachVictimOnce_andOnlyWhileResident() {
        var a = Map.of("qwen3:14b", VICTIM_MIB);
        var b = Map.of("qwen3:14b", VICTIM_MIB, "qwen3:8b", 6_000L);
        assertEquals(VICTIM_MIB + 6_000, TicketManager.evictionCreditMiB(List.of(a, b), Set.of("qwen3:14b", "qwen3:8b")));
        assertEquals(6_000, TicketManager.evictionCreditMiB(List.of(a, b), Set.of("qwen3:8b")),
                "a victim already unloaded is in the headroom, not the credit");
    }

    @Test
    void withinBudget_rules() {
        assertTrue(TicketManager.withinBudget(0, 12_000, 2_000, 14_000));
        assertFalse(TicketManager.withinBudget(12_000, 12_000, 2_000, 14_000));
        assertFalse(TicketManager.withinBudget(0, 0, 2_000, 14_000));
        assertFalse(TicketManager.withinBudget(0, 1, 0, 0));
    }

    @Test
    void secondPlannerOfTheSameVictim_isRefused() {
        var bucket = new InMemoryKv();
        var tickets = new TicketManager(bucket.provider(TicketManager.TICKETS_BUCKET), new ObjectMapper());

        var first = claim(tickets, "qwen3:32b-a");
        var second = claim(tickets, "qwen3:32b-b");

        assertTrue(first.isPresent());
        assertTrue(second.isEmpty(), "the victim's memory is already spoken for");
        assertEquals(Map.of("qwen3:14b", VICTIM_MIB), tickets.plannedEvictionsOn(GPU));
    }

    @RepeatedTest(20)
    void concurrentPlanners_neverBothWin() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = new TicketManager(bucket.provider(TicketManager.TICKETS_BUCKET), new ObjectMapper());
        var barrier = new CyclicBarrier(2);

        var a = CompletableFuture.supplyAsync(() -> { await(barrier); return claim(tickets, "model-a"); });
        var b = CompletableFuture.supplyAsync(() -> { await(barrier); return claim(tickets, "model-b"); });

        int winners = (a.get().isPresent() ? 1 : 0) + (b.get().isPresent() ? 1 : 0);
        assertTrue(winners <= 1, "two loads cannot both spend one victim's memory");
    }

    @Test
    void claimIsReleasedWhenTheVictimPicksUpWork() {
        var bucket = new InMemoryKv();
        var tickets = new TicketManager(bucket.provider(TicketManager.TICKETS_BUCKET), new ObjectMapper());
        // A warm call on the victim is already in flight.
        assertTrue(tickets.claim(GPU, 512, 0, HEADROOM, "qwen3:14b").isPresent());

        assertTrue(claim(tickets, "qwen3:32b").isEmpty());
    }

    private static Optional<Ticket> claim(TicketManager tickets, String model) {
        return tickets.claimWithEvictions(GPU, LOAD_MIB, 0, HEADROOM, model,
                Map.of("qwen3:14b", VICTIM_MIB), Set.of("qwen3:14b"));
    }

    private static void await(CyclicBarrier barrier) {
        try {
            barrier.await();
        } catch (Exception e) {
            throw new IllegalStateException(e);
        }
    }
}
