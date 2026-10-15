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
 * the provider has slots; the rest release and move on to the next candidate. The
 * same holds for a copy that is still loading: claimers that lose its slots take an
 * in-tolerance model that can start now, and queue on the loading copy only when
 * none can.
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

    /** A GPU with nothing resident: a cold start. */
    private static ProviderState emptyGpu(String name, int maxParallel) {
        return new ProviderState(name, "http://" + name, maxParallel, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of());
    }

    /** rig1 with nothing resident; the 32b does not fit it, the 14b does. */
    private static ProviderState emptyRig1() {
        return new ProviderState(RIG1, "http://rig1", 4, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of());
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

    @Test
    void claimLoad_keepsTheEarliestLoad_andReleasesALaterOneForTheSameModel() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var serving = java.util.Set.of(GPU, RIG1);

        var first = tickets.claimLoad(RIG1, SMALL_MIB, 0, 24_564, SMALL_MODEL, serving);
        var second = tickets.claimLoad(RIG1, 512, 0, 24_564, SMALL_MODEL, serving);
        var otherModel = tickets.claimLoad(GPU, BIG_MIB, 0, 32_607, BIG_MODEL, serving);

        assertTrue(first.isPresent());
        assertTrue(second.isEmpty(), "the earlier claim is loading the 14b; the later one joins it");
        assertTrue(otherModel.isPresent(), "a load of another model is not a duplicate");
        assertEquals(1, ticketsOn(bucket, RIG1));
    }

    @Test
    void claimLoad_releasesWhenAnEarlierLoadOfTheModelIsOnAnotherServingProvider() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim("ollama-gpu2", BIG_MIB, 0, 32_607, BIG_MODEL).orElseThrow();

        assertTrue(tickets.claimLoad(GPU, BIG_MIB, 0, 32_607, BIG_MODEL, java.util.Set.of(GPU, "ollama-gpu2"))
                .isEmpty(), "one load of the 32b is already under way on another GPU");
        assertEquals(0, ticketsOn(bucket, GPU));
    }

    @Test
    void claimLoad_ignoresACopyOnAProviderThatCannotServeTheCall() {
        var tickets = tickets(new InMemoryKv());
        tickets.claim(RIG1, SMALL_MIB, 0, 24_564, SMALL_MODEL).orElseThrow();

        assertTrue(tickets.claimLoad(GPU, SMALL_MIB, 0, 32_607, SMALL_MODEL, java.util.Set.of(GPU)).isPresent(),
                "rig1's copy is not among the providers that can serve this call");
    }

    @Test
    void claimLoad_withoutAModelName_isAPlainClaim_andNeedsNats() {
        assertTrue(tickets(new InMemoryKv()).claimLoad(GPU, 512, 0, 5_000, null, java.util.Set.of(GPU)).isPresent());
        var down = new TicketManager(mock(NatsConnectionProvider.class), new ObjectMapper());
        assertTrue(down.claimLoad(GPU, 512, 0, 5_000, BIG_MODEL, java.util.Set.of(GPU)).isEmpty());
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

    // ---- joining a copy that is still loading ----

    private static List<CallPlanner.Placement> placedOn(List<Optional<CallPlanner.Placement>> placed, String provider) {
        return placed.stream().map(Optional::orElseThrow).filter(p -> p.pick().provider().name().equals(provider))
                .toList();
    }

    /** Another agent's cold load of the 32b is in flight on the gpu and holds its one slot. */
    private static void loadInFlight(TicketManager tickets) {
        tickets.claim(GPU, BIG_MIB, 0, 32_607, BIG_MODEL).orElseThrow();
    }

    @RepeatedTest(20)
    void losersOfALoadingCopysSlot_takeAnInToleranceModelWarmElsewhere() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        loadInFlight(tickets);
        var states = List.of(emptyGpu(GPU, 1), rig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        List<Optional<CallPlanner.Placement>> placed =
                concurrently(3, () -> planner.place(request(List.of(SMALL_MODEL), states)));

        assertTrue(placed.stream().allMatch(Optional::isPresent));
        assertEquals(3, placedOn(placed, RIG1).size(), "the loading copy's one slot is spoken for");
        assertTrue(placedOn(placed, RIG1).stream().allMatch(p -> SMALL_MODEL.equals(p.model()) && !p.coldLoad()));
        assertEquals(1, ticketsOn(bucket, GPU), "only the load in flight holds the gpu");
    }

    @RepeatedTest(20)
    void coldStart_oneLoadsThePreferredModel_theRestLoadAnInToleranceModelElsewhere() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(emptyGpu(GPU, 1), emptyRig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        var loader = planner.place(request(List.of(SMALL_MODEL), states)).orElseThrow();
        List<Optional<CallPlanner.Placement>> joiners =
                concurrently(3, () -> planner.place(request(List.of(SMALL_MODEL), states)));

        assertEquals(GPU, loader.pick().provider().name());
        assertEquals(BIG_MODEL, loader.model());
        assertTrue(joiners.stream().allMatch(Optional::isPresent));
        assertEquals(3, placedOn(joiners, RIG1).size(), "no joiner queues behind the gpu's one slot");
        assertTrue(placedOn(joiners, RIG1).stream().allMatch(p -> SMALL_MODEL.equals(p.model())));
        assertEquals(1, ticketsOn(bucket, GPU));
        assertEquals(3, ticketsOn(bucket, RIG1), "the 14b's four slots hold all three");
    }

    @RepeatedTest(20)
    void coldStart_concurrentPlanners_neverQueueBehindTheLoadingCopy_whileAnAlternativeCanLoad() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(emptyGpu(GPU, 1), emptyRig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        List<Optional<CallPlanner.Placement>> placed =
                concurrently(4, () -> planner.place(request(List.of(SMALL_MODEL), states)));

        assertTrue(placed.stream().allMatch(Optional::isPresent));
        assertTrue(placedOn(placed, GPU).size() <= 1, "at most one call holds the gpu's one slot");
        assertTrue(placedOn(placed, GPU).stream().allMatch(p -> BIG_MODEL.equals(p.model())));
        assertTrue(placedOn(placed, RIG1).stream().allMatch(p -> SMALL_MODEL.equals(p.model())));
        assertTrue(ticketsOn(bucket, GPU) <= 1);
    }

    @Test
    void noAlternative_losersQueueOnTheLoadingCopy() throws Exception {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        loadInFlight(tickets);
        var states = List.of(emptyGpu(GPU, 1), emptyRig1());
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        List<Optional<CallPlanner.Placement>> placed =
                concurrently(3, () -> planner.place(request(List.of(), states)));

        assertEquals(3, placedOn(placed, GPU).size(), "queueing at the loading copy is the last resort");
        assertEquals(4, ticketsOn(bucket, GPU));
        assertEquals(0, ticketsOn(bucket, RIG1));
    }

    @Test
    void alternativeThatCannotHoldThePrompt_doesNotStopTheQueueOnTheLoadingCopy() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        loadInFlight(tickets);
        var smallContext = new ProviderState(RIG1, "http://rig1", 4, 0, 0, List.of(SMALL_MODEL), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of(SMALL_MODEL, SMALL_MIB), Map.of(), 8_192, Map.of());
        var states = List.of(emptyGpu(GPU, 1), smallContext);
        var planner = new CallPlanner(selector(states, tickets), tickets, null);
        var req = new CallPlanner.PlacementRequest(BIG_MODEL, List.of(SMALL_MODEL), states,
                m -> BIG_MODEL.equals(m) ? BIG_MIB : SMALL_MIB, m -> 256L, 11_000L);

        var placement = planner.place(req).orElseThrow();

        assertEquals(GPU, placement.pick().provider().name(), "rig1's 8K context cannot hold an 11K prompt");
        assertEquals(0, ticketsOn(bucket, RIG1));
    }

    private static final String GPU2 = "ollama-gpu2";

    @Test
    void convergence_aSecondCallJoinsTheLoadingCopyWithAFreeSlot_insteadOfLoadingAnotherCopy() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(emptyGpu(GPU, 4), emptyGpu(GPU2, 4));
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        var first = planner.place(request(List.of(), states)).orElseThrow();
        var second = planner.place(request(List.of(), states)).orElseThrow();

        assertTrue(first.coldLoad());
        assertEquals(first.pick().provider().name(), second.pick().provider().name());
        String other = GPU.equals(first.pick().provider().name()) ? GPU2 : GPU;
        assertEquals(0, ticketsOn(bucket, other), "one load serves both calls");
    }

    @Test
    void convergence_aLoserOfTheLoadingCopysSlot_doesNotLoadTheSameModelOnAnotherGpu() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        var states = List.of(emptyGpu(GPU, 1), emptyGpu(GPU2, 1));
        var planner = new CallPlanner(selector(states, tickets), tickets, null);

        var first = planner.place(request(List.of(), states)).orElseThrow();
        var second = planner.place(request(List.of(), states)).orElseThrow();

        assertEquals(first.pick().provider().name(), second.pick().provider().name(),
                "queueing one call behind a load costs less than a second load");
        String other = GPU.equals(first.pick().provider().name()) ? GPU2 : GPU;
        assertEquals(0, ticketsOn(bucket, other));
    }

    @Test
    void roomMode_admitsNoColdLoadWhileACopyIsLoading_andNoFullCopy() {
        var tickets = tickets(new InMemoryKv());
        loadInFlight(tickets);
        var sel = selector(List.of(emptyGpu(GPU, 1), emptyGpu(GPU2, 1)), tickets);

        assertTrue(sel.pickAndClaimRoom(BIG_MODEL, BIG_MIB, 0, 0).isEmpty());
    }

    @Test
    void roomMode_coldLoadsWhenNoCopyExists_andRefusesAnUnknownFootprint() {
        var tickets = tickets(new InMemoryKv());
        var sel = selector(List.of(emptyGpu(GPU, 1)), tickets);

        assertTrue(sel.pickAndClaimRoom(BIG_MODEL, 0, 0, 0).isEmpty(), "no footprint, no placement");
        var pick = sel.pickAndClaimRoom(BIG_MODEL, BIG_MIB, 0, 0).orElseThrow();
        assertEquals(GPU, pick.provider().name());
    }
}
