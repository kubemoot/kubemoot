package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.RepeatedTest;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.CyclicBarrier;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Supplier;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.Mockito.mock;

/**
 * Concurrent claims that all read the same free slot keep only as many tickets as
 * the provider has slots; the rest release and move on to the next candidate.
 */
class WarmSlotClaimTest {

    private static final String GPU = "ollama-gpu";
    private static final String RIG1 = "ollama-rig1";
    private static final String BIG_MODEL = "qwen3:32b";
    private static final String SMALL_MODEL = "qwen3:14b";
    private static final long BIG_MIB = 27_252;
    private static final long SMALL_MIB = 12_224;

    private static ProviderState gpu(int maxParallel) {
        return new ProviderState(GPU, "http://gpu", maxParallel, 0, 0, List.of(BIG_MODEL), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of(BIG_MODEL, BIG_MIB));
    }

    private static ProviderState rig1() {
        return new ProviderState(RIG1, "http://rig1", 4, 0, 0, List.of(SMALL_MODEL), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of(SMALL_MODEL, SMALL_MIB));
    }

    private static TicketManager tickets(InMemoryKv bucket) {
        return new TicketManager(bucket.provider(TicketManager.TICKETS_BUCKET), new ObjectMapper());
    }

    /** A selector over fixed provider state, with real tickets and the Ollama slot rule. */
    private static ProviderSelector selector(List<ProviderState> states, TicketManager tickets) {
        return new ProviderSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ProviderState> readState() {
                return states;
            }
        };
    }

    private static long ticketsOn(InMemoryKv bucket, String provider) {
        return bucket.data.keySet().stream().filter(k -> k.startsWith(TicketManager.keyPrefixFor(provider))).count();
    }

    /** Runs {@code n} copies of {@code task} released together by a barrier. */
    private static <T> List<T> concurrently(int n, Supplier<T> task) throws Exception {
        ExecutorService pool = Executors.newFixedThreadPool(n);
        try {
            CyclicBarrier barrier = new CyclicBarrier(n);
            List<Future<T>> futures = new ArrayList<>();
            for (int i = 0; i < n; i++) {
                futures.add(pool.submit(() -> {
                    barrier.await(5, TimeUnit.SECONDS);
                    return task.get();
                }));
            }
            List<T> results = new ArrayList<>();
            for (Future<T> f : futures) {
                results.add(f.get(10, TimeUnit.SECONDS));
            }
            return results;
        } finally {
            pool.shutdownNow();
        }
    }

    // ---- the ticket manager ----

    @Test
    void claimSlot_keepsTheEarliestClaim_andReleasesALaterOne() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);

        var first = tickets.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> ahead < 1);
        var second = tickets.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> ahead < 1);

        assertTrue(first.isPresent());
        assertTrue(second.isEmpty(), "the one slot is held by the earlier claim");
        assertEquals(1, ticketsOn(bucket, GPU), "the losing ticket is released");
    }

    /** Records the calls-ahead count the slot check saw, and allows the claim. */
    private static int aheadSeenBy(TicketManager tickets) {
        AtomicInteger seen = new AtomicInteger(-1);
        tickets.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> {
            seen.set(ahead);
            return true;
        }).orElseThrow();
        return seen.get();
    }

    @Test
    void claimSlot_countsOnlyEarlierTicketsOnTheSameProvider() {
        var tickets = tickets(new InMemoryKv());
        tickets.claim(GPU, 512, 0, 5_000, BIG_MODEL).orElseThrow();
        tickets.claim(RIG1, 512, 0, 5_000, SMALL_MODEL).orElseThrow();

        assertEquals(1, aheadSeenBy(tickets), "the other provider's ticket is not ahead");
    }

    @Test
    void claimSlot_doesNotCountAReleasedTicket() {
        var tickets = tickets(new InMemoryKv());
        tickets.release(tickets.claim(GPU, 512, 0, 5_000, BIG_MODEL).orElseThrow());

        assertEquals(0, aheadSeenBy(tickets));
    }

    @Test
    void claimSlot_emptyWhenNatsIsUnavailable() {
        var down = new TicketManager(mock(NatsConnectionProvider.class), new ObjectMapper());
        assertTrue(down.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> true).isEmpty());
    }

    @Test
    void claimSlot_emptyWithoutTheTicketsBucket() {
        var noBucket = new TicketManager(new InMemoryKv().provider("other-bucket"), new ObjectMapper());
        assertTrue(noBucket.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> true).isEmpty());
    }

    @Test
    void claimSlot_stillAppliesTheVramBudget() {
        var bucket = new InMemoryKv();
        assertTrue(tickets(bucket).claimSlot(GPU, 512, 0, 100, BIG_MODEL, ahead -> true).isEmpty());
        assertTrue(bucket.data.isEmpty());
    }

    // ---- the selector ----

    // The gpu's headroom (32607 - 27252 = 5355 MiB) holds every concurrent 512 MiB
    // scratch claim below, so only the slot check decides who keeps a ticket.

    @RepeatedTest(20)
    void concurrentWarmClaims_oneSlot_exactlyOneKeepsItsTicket() throws Exception {
        var bucket = new InMemoryKv();
        var sel = selector(List.of(gpu(1)), tickets(bucket));

        List<Optional<Pick>> picks = concurrently(8, () -> sel.pickAndClaimWarm(BIG_MODEL, BIG_MIB, 0));

        assertEquals(1, picks.stream().filter(Optional::isPresent).count());
        assertEquals(1, ticketsOn(bucket, GPU));
    }

    @RepeatedTest(20)
    void concurrentWarmClaims_fourSlots_exactlyFourKeepTheirTickets() throws Exception {
        var bucket = new InMemoryKv();
        var sel = selector(List.of(gpu(4)), tickets(bucket));

        List<Optional<Pick>> picks = concurrently(6, () -> sel.pickAndClaimWarm(BIG_MODEL, BIG_MIB, 0));

        assertEquals(4, picks.stream().filter(Optional::isPresent).count());
        assertEquals(4, ticketsOn(bucket, GPU));
    }

    @Test
    void weightedCostAndLoadingClaims_queueBeyondTheSlots() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim(GPU, 512, 0, 5_000, BIG_MODEL).orElseThrow();

        assertTrue(selector(List.of(gpu(1)), tickets).pickAndClaim(BIG_MODEL, BIG_MIB, 0).isPresent(),
                "the weighted cost queues at a busy warm copy");
        var loading = new ProviderState(GPU, "http://gpu", 1, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of());
        assertTrue(selector(List.of(loading), tickets).pickAndClaimLoading(BIG_MODEL, BIG_MIB, 0).isPresent(),
                "a call converges on a copy being loaded");
        assertEquals(3, ticketsOn(bucket, GPU));
    }

    // ---- the planner ----

    private static CallPlanner.PlacementRequest request(List<String> alternatives, List<ProviderState> states) {
        return new CallPlanner.PlacementRequest(BIG_MODEL, alternatives, states,
                m -> BIG_MODEL.equals(m) ? BIG_MIB : SMALL_MIB, m -> 256L);
    }

    @RepeatedTest(20)
    void losersOfTheOneSlot_fallThroughToAnInToleranceModelWithFreeSlots() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(gpu(1), rig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        List<Optional<CallPlanner.Placement>> placed =
                concurrently(4, () -> planner.place(request(List.of(SMALL_MODEL), states)));

        assertTrue(placed.stream().allMatch(Optional::isPresent));
        var onGpu = placed.stream().map(Optional::get).filter(p -> p.pick().provider().name().equals(GPU)).toList();
        var onRig1 = placed.stream().map(Optional::get).filter(p -> p.pick().provider().name().equals(RIG1)).toList();
        assertEquals(1, onGpu.size(), "one call takes the 32b's only slot");
        assertEquals(BIG_MODEL, onGpu.get(0).model());
        assertEquals(3, onRig1.size(), "the rest take the 14b's free slots");
        assertTrue(onRig1.stream().allMatch(p -> SMALL_MODEL.equals(p.model()) && !p.coldLoad()));
        assertEquals(1, ticketsOn(bucket, GPU));
        assertEquals(3, ticketsOn(bucket, RIG1));
    }

    @Test
    void loserWithNoAlternative_goesOnToTheWeightedCost_whichQueuesAtTheWarmCopy() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(gpu(1), rig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        var first = planner.place(request(List.of(), states)).orElseThrow();
        var second = planner.place(request(List.of(), states)).orElseThrow();

        assertEquals(GPU, first.pick().provider().name());
        assertEquals(GPU, second.pick().provider().name(),
                "the 32b fits only the gpu, so queueing at its copy is the cheapest placement");
        assertEquals(2, ticketsOn(bucket, GPU), "the queued call holds a ticket, so the next estimate counts it");
    }
}
