package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.nats.NatsLink;
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
 * the server has slots; the rest release and move on to the next candidate. The
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

    /** Both test models are downloaded on every test server. */
    private static final Map<String, Long> DOWNLOADED = Map.of(BIG_MODEL, BIG_MIB, SMALL_MODEL, SMALL_MIB);

    private static ServerState gpu(int maxParallel) {
        return new ServerState(GPU, "http://gpu", maxParallel, 0, 0, List.of(BIG_MODEL), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of(BIG_MODEL, BIG_MIB), DOWNLOADED);
    }

    private static ServerState rig1() {
        return new ServerState(RIG1, "http://rig1", 4, 0, 0, List.of(SMALL_MODEL), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of(SMALL_MODEL, SMALL_MIB), DOWNLOADED);
    }

    /** A GPU with nothing resident: a cold start. */
    private static ServerState emptyGpu(String name, int maxParallel) {
        return new ServerState(name, "http://" + name, maxParallel, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of(), DOWNLOADED);
    }

    /** rig1 with nothing resident; the 32b does not fit it, the 14b does. */
    private static ServerState emptyRig1() {
        return new ServerState(RIG1, "http://rig1", 4, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of(), DOWNLOADED);
    }

    private static TicketManager tickets(InMemoryKv bucket) {
        return new TicketManager(bucket.server(TicketManager.TICKETS_BUCKET), new ObjectMapper());
    }

    /** A selector over fixed server state, with real tickets and the Ollama slot rule. */
    private static ServerSelector selector(List<ServerState> states, TicketManager tickets) {
        return new ServerSelector(null, new ObjectMapper(), tickets, null, new OllamaDriver(null)) {
            @Override
            public List<ServerState> readState() {
                return states;
            }
        };
    }

    private static long ticketsOn(InMemoryKv bucket, String server) {
        return bucket.data.keySet().stream().filter(k -> k.startsWith(TicketManager.keyPrefixFor(server))).count();
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
    void claimSlot_countsOnlyEarlierTicketsOnTheSameServer() {
        var tickets = tickets(new InMemoryKv());
        tickets.claim(GPU, 512, 0, 5_000, BIG_MODEL).orElseThrow();
        tickets.claim(RIG1, 512, 0, 5_000, SMALL_MODEL).orElseThrow();

        assertEquals(1, aheadSeenBy(tickets), "the other server's ticket is not ahead");
    }

    @Test
    void claimSlot_doesNotCountAReleasedTicket() {
        var tickets = tickets(new InMemoryKv());
        tickets.release(tickets.claim(GPU, 512, 0, 5_000, BIG_MODEL).orElseThrow());

        assertEquals(0, aheadSeenBy(tickets));
    }

    @Test
    void claimSlot_emptyWhenNatsIsUnavailable() {
        var down = new TicketManager(mock(NatsLink.class), new ObjectMapper());
        assertTrue(down.claimSlot(GPU, 512, 0, 5_000, BIG_MODEL, ahead -> true).isEmpty());
    }

    @Test
    void claimSlot_emptyWithoutTheTicketsBucket() {
        var noBucket = new TicketManager(new InMemoryKv().server("other-bucket"), new ObjectMapper());
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
    void claimLoad_releasesWhenAnEarlierLoadOfTheModelIsOnAnotherServingServer() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim("ollama-gpu2", BIG_MIB, 0, 32_607, BIG_MODEL).orElseThrow();

        assertTrue(tickets.claimLoad(GPU, BIG_MIB, 0, 32_607, BIG_MODEL, java.util.Set.of(GPU, "ollama-gpu2"))
                .isEmpty(), "one load of the 32b is already under way on another GPU");
        assertEquals(0, ticketsOn(bucket, GPU));
    }

    @Test
    void claimLoad_ignoresACopyOnAServerThatCannotServeTheCall() {
        var tickets = tickets(new InMemoryKv());
        tickets.claim(RIG1, SMALL_MIB, 0, 24_564, SMALL_MODEL).orElseThrow();

        assertTrue(tickets.claimLoad(GPU, SMALL_MIB, 0, 32_607, SMALL_MODEL, java.util.Set.of(GPU)).isPresent(),
                "rig1's copy is not among the servers that can serve this call");
    }

    @Test
    void claimLoad_withoutAModelName_isAPlainClaim_andNeedsNats() {
        assertTrue(tickets(new InMemoryKv()).claimLoad(GPU, 512, 0, 5_000, null, java.util.Set.of(GPU)).isPresent());
        var down = new TicketManager(mock(NatsLink.class), new ObjectMapper());
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
        var loading = new ServerState(GPU, "http://gpu", 1, 0, 0, List.of(), true,
                "2026-10-15T00:00:00Z", 32_607, Map.of(), DOWNLOADED);
        assertTrue(selector(List.of(loading), tickets).pickAndClaimLoading(BIG_MODEL, BIG_MIB, 0).isPresent(),
                "a call converges on a copy being loaded");
        assertEquals(3, ticketsOn(bucket, GPU));
    }

    // ---- the planner ----

    private static CallPlanner.PlacementRequest request(List<String> alternatives, List<ServerState> states) {
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
        var onGpu = placed.stream().map(Optional::get).filter(p -> p.pick().server().name().equals(GPU)).toList();
        var onRig1 = placed.stream().map(Optional::get).filter(p -> p.pick().server().name().equals(RIG1)).toList();
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

        assertEquals(GPU, first.pick().server().name());
        assertEquals(GPU, second.pick().server().name(),
                "the 32b fits only the gpu, so queueing at its copy is the cheapest placement");
        assertEquals(2, ticketsOn(bucket, GPU), "the queued call holds a ticket, so the next estimate counts it");
        assertFalse(first.queued(), "the first call holds the 32b's slot");
        assertTrue(second.queued(), "the second waits behind it");
    }

    // ---- joining a copy that is still loading ----

    private static List<CallPlanner.Placement> placedOn(List<Optional<CallPlanner.Placement>> placed, String server) {
        return placed.stream().map(Optional::orElseThrow).filter(p -> p.pick().server().name().equals(server))
                .toList();
    }

    /** Each placement as model@server with its flags, and each ticket's model and footprint: for failure messages. */
    private static Supplier<String> describe(List<Optional<CallPlanner.Placement>> placed, InMemoryKv bucket) {
        return () -> placed.stream().map(o -> o.map(p -> p.model() + "@" + p.pick().server().name()
                        + (p.queued() ? " queued" : "") + (p.coldLoad() ? " cold" : "")).orElse("none")).toList()
                + " tickets " + bucket.data.entrySet().stream().map(e -> e.getKey() + "=" + ticketSummary(e.getValue()))
                .toList();
    }

    private static String ticketSummary(byte[] value) {
        try {
            var n = new ObjectMapper().readTree(value);
            return n.path("modelName").asText("?") + "/" + n.path("modelFootprintMiB").asLong();
        } catch (java.io.IOException e) {
            return "unreadable";
        }
    }

    @Test
    void aFullLoadOfAModelThatHasASlotTicket_addsOnlyTheDifference() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim(RIG1, ServerSelector.WARM_SCRATCH_MIB, 0, 24_564, SMALL_MODEL).orElseThrow();

        assertTrue(tickets.claim(RIG1, SMALL_MIB, 0, SMALL_MIB, SMALL_MODEL).isPresent(),
                "the model's memory is its largest ticket, not the slot ticket plus the load");
        assertTrue(tickets.claim(RIG1, SMALL_MIB + 1, 0, SMALL_MIB, SMALL_MODEL).isEmpty(),
                "a larger ticket of the model must still fit as a whole");
    }

    @Test
    void ticketsWithoutAModel_eachCountInFull() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim(GPU, 10_000, 0, 32_607, null).orElseThrow();
        tickets.claim(GPU, 10_000, 0, 32_607, null).orElseThrow();

        assertEquals(20_000, tickets.activeFootprintFor(GPU));
        assertTrue(tickets.claim(GPU, 13_000, 0, 32_607, null).isEmpty());
        assertTrue(tickets.claim(GPU, 12_000, 0, 32_607, null).isPresent());
    }

    @Test
    void aDuplicateLoadTicketOfTheSameModel_doesNotFillTheServer() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        // Two claimers wrote a ticket to load the 14b on rig1 at once; the later one is
        // about to release. Until it does, rig1 must not look full: the model loads once.
        tickets.claim(RIG1, SMALL_MIB, 0, 24_564, SMALL_MODEL).orElseThrow();
        tickets.claim(RIG1, SMALL_MIB, 0, 24_564, SMALL_MODEL).orElseThrow();

        assertEquals(SMALL_MIB, tickets.activeFootprintFor(RIG1));
        var join = tickets.claimSlot(RIG1, ServerSelector.WARM_SCRATCH_MIB, 0, 24_564, SMALL_MODEL,
                ahead -> ahead < 4);
        assertTrue(join.isPresent(), "a call joining the loading copy fits beside the duplicate");
    }

    @Test
    void aSecondModelStillNeedsItsOwnRoom() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        tickets.claim(GPU, BIG_MIB, 0, 32_607, BIG_MODEL).orElseThrow();

        assertTrue(tickets.claim(GPU, SMALL_MIB, 0, 32_607, SMALL_MODEL).isEmpty(),
                "a different model adds its whole footprint");
        assertTrue(tickets.claim(GPU, BIG_MIB, 0, 32_607, BIG_MODEL).isPresent(),
                "the same model again adds nothing");
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

        Supplier<String> dump = describe(joiners, bucket);
        assertEquals(GPU, loader.pick().server().name());
        assertEquals(BIG_MODEL, loader.model());
        assertFalse(loader.queued(), "a load the call starts is not a queue");
        assertTrue(joiners.stream().allMatch(Optional::isPresent), dump);
        assertTrue(joiners.stream().map(Optional::orElseThrow).noneMatch(CallPlanner.Placement::queued), dump);
        assertEquals(3, placedOn(joiners, RIG1).size(), () -> "no joiner queues behind the gpu's one slot: " + dump.get());
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

        Supplier<String> dump = describe(placed, bucket);
        assertTrue(placed.stream().allMatch(Optional::isPresent), dump);
        assertTrue(placedOn(placed, GPU).size() <= 1, "at most one call holds the gpu's one slot");
        assertTrue(placedOn(placed, GPU).stream().allMatch(p -> BIG_MODEL.equals(p.model())), dump);
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
        assertTrue(placedOn(placed, GPU).stream().allMatch(CallPlanner.Placement::queued));
        assertEquals(4, ticketsOn(bucket, GPU));
        assertEquals(0, ticketsOn(bucket, RIG1));
    }

    @Test
    void alternativeThatCannotHoldThePrompt_doesNotStopTheQueueOnTheLoadingCopy() {
        var bucket = new InMemoryKv();
        var tickets = tickets(bucket);
        loadInFlight(tickets);
        var smallContext = new ServerState(RIG1, "http://rig1", 4, 0, 0, List.of(SMALL_MODEL), true,
                "2026-10-15T00:00:00Z", 24_564, Map.of(SMALL_MODEL, SMALL_MIB), Map.of(), 8_192, Map.of());
        var states = List.of(emptyGpu(GPU, 1), smallContext);
        var planner = new CallPlanner(selector(states, tickets), tickets, null);
        var req = new CallPlanner.PlacementRequest(BIG_MODEL, List.of(SMALL_MODEL), states,
                m -> BIG_MODEL.equals(m) ? BIG_MIB : SMALL_MIB, m -> 256L, 11_000L);

        var placement = planner.place(req).orElseThrow();

        assertEquals(GPU, placement.pick().server().name(), "rig1's 8K context cannot hold an 11K prompt");
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
        assertEquals(first.pick().server().name(), second.pick().server().name());
        String other = GPU.equals(first.pick().server().name()) ? GPU2 : GPU;
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

        assertEquals(first.pick().server().name(), second.pick().server().name(),
                "queueing one call behind a load costs less than a second load");
        String other = GPU.equals(first.pick().server().name()) ? GPU2 : GPU;
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
        assertEquals(GPU, pick.server().name());
    }
}
