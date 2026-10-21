package ai.kubemoot.placement;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import jakarta.ws.rs.core.Response;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

import static ai.kubemoot.placement.PlacementFixture.MODEL;
import static ai.kubemoot.placement.PlacementFixture.SERVER;
import static ai.kubemoot.placement.PlacementFixture.body;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The placement API over the real placement code: where a call runs, the ticket it holds, the plan
 * made at selection and handed to the first call, the refusals no wait cures, and the capacity wait.
 */
class PlacementApiTest {

    private PlacementFixture f;
    private PlacementResource api;

    @BeforeEach
    void setUp() {
        f = new PlacementFixture();
        f.publishWarmServer();
        api = f.resource;
    }

    private static JsonNode entity(Response r) {
        return (JsonNode) r.getEntity();
    }

    // ---- place and release ----

    @Test
    void aCallIsPlacedOnTheWarmServer_withATicketAndItsExplanation() {
        Response r = api.place(body("triager"));

        assertEquals(200, r.getStatus());
        JsonNode reply = entity(r);
        assertEquals(SERVER, reply.path("server").asText());
        assertEquals(MODEL, reply.path("model").asText());
        assertEquals("http://rig0:11434", reply.path("endpoint").asText());
        assertEquals(SERVER, reply.path("ticket").path("server").asText());
        assertNotNull(reply.path("ticket").path("ticketId").asText(null));
        assertEquals(1, reply.path("active").asInt(), "the caller learns the load without asking again");
        assertEquals("algorithm", reply.path("decision").path("policy").asText(),
                "the receipt and prediction-error scoring read this decision");
        assertFalse(reply.path("decision").path("reason").asText().isBlank());
        assertEquals(1, f.table.scan("rig0__").size());
    }

    @Test
    void releasingTheTicketGivesTheSlotBack_andReleasingTwiceIsHarmless() {
        String id = entity(api.place(body("a"))).path("ticket").path("ticketId").asText();
        ObjectNode release = PlacementFixture.M.createObjectNode().put("ticket", id);

        assertEquals(204, api.release(release).getStatus());
        assertEquals(204, api.release(release).getStatus());
        assertTrue(f.table.scan("rig0__").isEmpty());
    }

    @Test
    void callsFromDifferentAgentsShareOneTableOfTickets() {
        api.place(body("a"));
        api.place(body("b"));
        assertEquals(2, f.tickets.activeCountFor(SERVER));
        assertEquals(2, entity(api.tickets()).size());
    }

    @Test
    void aRequestThatNamesNoModelIsRefused() {
        ObjectNode empty = PlacementFixture.identity("a");
        empty.putArray("models");
        assertThrows(ErrorMappers.InvalidRequestException.class, () -> api.place(empty));
    }

    @Test
    void aRequestForAnotherPolicyIsPlacedByThatPolicy() {
        ObjectNode shadow = body("shadow-agent");
        shadow.put("policy", "shadow");
        JsonNode reply = entity(api.place(shadow));
        assertEquals("shadow", reply.path("decision").path("policy").asText());
        assertTrue(reply.path("decision").path("reason").asText().contains("learned policy"),
                "a shadow decision says what the learned policy would have chosen");
    }

    // ---- refusals ----

    @Test
    void aModelNoAcceleratorCanHoldIsRefusedAsTooLarge() {
        Response r = api.place(body("a", "t", "huge:405b", 400_000, 100, true));
        assertEquals(409, r.getStatus());
        assertEquals("model-too-large", entity(r).path("code").asText());
    }

    @Test
    void aPromptNoContextHoldsIsRefusedAsTooLarge() {
        Response r = api.place(body("a", "t", MODEL, 10_000, 90_000, true));
        assertEquals(409, r.getStatus());
        assertEquals("prompt-too-large", entity(r).path("code").asText());
    }

    @Test
    void aModelNoServerHasIsRefused() {
        f.publishServer(SERVER, 32_768, 2, Map.of(), Map.of(), 8_192);
        Response r = api.place(body("a", "t", MODEL, 10_000, 100, true));
        assertEquals(409, r.getStatus());
        assertEquals("no-server-has-model", entity(r).path("code").asText());
    }

    /** A small server holding the warm model, with a second model downloaded that does not fit beside it. */
    private void publishCrowdedServer() {
        f.publishServer(SERVER, 12_000, 2, Map.of(MODEL, 10_000L), Map.of(MODEL, 10_000L, "other:8b", 8_000L), 8_192);
    }

    @Test
    void aBusyClusterIsNoFitNotARefusal() {
        publishCrowdedServer();
        // other:8b is downloaded and could run here once room is made, but this caller must not unload anything.
        Response busy = api.place(body("c", "t", "other:8b", 8_000, 100, false));
        assertEquals(409, busy.getStatus());
        assertEquals("kubemoot_no_fit", entity(busy).path("code").asText(),
                "busy is the answer a wait can cure, not a refusal");
        assertTrue(f.table.scan("rig0__").isEmpty(), "a refused call holds nothing");
    }

    @Test
    void aCallThatMayUnloadMakesRoom_andTheReplyNamesWhatWasUnloaded() {
        publishCrowdedServer();
        Response r = api.place(body("c", "t", "other:8b", 8_000, 100, true));
        assertEquals(200, r.getStatus());
        JsonNode reply = entity(r);
        assertEquals("other:8b", reply.path("model").asText());
        assertEquals(MODEL, reply.path("evicted").path(0).asText());
        assertTrue(reply.path("coldLoad").asBoolean());
    }

    @Test
    void waitingPlacesTheCallOnceTheHolderReleases() {
        publishCrowdedServer();
        String holder = entity(api.place(body("a"))).path("ticket").path("ticketId").asText();
        Response blocked = api.place(body("c", "t", "other:8b", 8_000, 100, true));
        assertEquals(409, blocked.getStatus(), "the warm model is in use, so it cannot be unloaded");

        api.release(PlacementFixture.M.createObjectNode().put("ticket", holder));
        assertEquals(200, api.place(body("c", "t", "other:8b", 8_000, 100, true)).getStatus());
    }

    // ---- the plan made at selection ----

    @Test
    void aPlanMadeAtSelectionHoldsATicketUntilTheFirstCallTakesIt() {
        ObjectNode plan = body("planner", "thread-1", MODEL, 10_000, 500, true);
        plan.putArray("candidates").add(MODEL);
        assertEquals(202, api.plan("thread-1", plan).getStatus());
        assertEquals(1, f.tickets.activeCountFor(SERVER), "the plan holds the capacity");

        Response taken = api.take("thread-1", PlacementFixture.identity("planner"));
        assertEquals(200, taken.getStatus());
        assertEquals(SERVER, entity(taken).path("server").asText());
        assertEquals(1, f.tickets.activeCountFor(SERVER), "the first call owns the ticket now");

        assertEquals(204, api.take("thread-1", PlacementFixture.identity("planner")).getStatus(),
                "a plan is handed over once");
    }

    @Test
    void releasingTheThreadReleasesItsPlan() {
        ObjectNode plan = body("planner", "thread-2", MODEL, 10_000, 500, true);
        plan.putArray("candidates").add(MODEL);
        api.plan("thread-2", plan);
        assertEquals(1, f.tickets.activeCountFor(SERVER));

        assertEquals(204, api.releaseThread("thread-2", "crew-ns", "pilot", "planner").getStatus());
        assertEquals(0, f.tickets.activeCountFor(SERVER));
        assertEquals(204, api.take("thread-2", PlacementFixture.identity("planner")).getStatus());
    }

    @Test
    void aThreadWithoutAPlanHasNothingToTake() {
        assertEquals(204, api.take("nobody-planned", PlacementFixture.identity("planner")).getStatus());
    }

    @Test
    void plansOfDifferentAgentsDoNotMix() {
        ObjectNode plan = body("planner-a", "thread-3", MODEL, 10_000, 500, true);
        plan.putArray("candidates").add(MODEL);
        api.plan("thread-3", plan);
        assertEquals(204, api.take("thread-3", PlacementFixture.identity("planner-b")).getStatus());
        assertEquals(200, api.take("thread-3", PlacementFixture.identity("planner-a")).getStatus());
    }

    // ---- waiting for capacity ----

    @Test
    void theCapacityVersionMovesWhenATicketIsClaimedOrReleased() {
        long before = entity(api.capacity()).path("version").asLong();
        String id = entity(api.place(body("a"))).path("ticket").path("ticketId").asText();
        long claimed = entity(api.capacity()).path("version").asLong();
        api.release(PlacementFixture.M.createObjectNode().put("ticket", id));
        long released = entity(api.capacity()).path("version").asLong();
        assertTrue(claimed > before);
        assertTrue(released > claimed);
    }

    @Test
    void aWaitForCapacityEndsAtTheNextChange_withoutHoldingAThread() throws Exception {
        long seen = f.capacity.current();
        ObjectNode await = PlacementFixture.M.createObjectNode().put("seen", seen).put("maxWaitMs", 20_000);
        CompletableFuture<Response> pending = api.awaitCapacity(await).toCompletableFuture();
        assertFalse(pending.isDone());
        assertEquals(1, f.capacity.waiting());

        api.place(body("a"));
        Response r = pending.get(2, TimeUnit.SECONDS);
        assertTrue(entity(r).path("changed").asBoolean());
        assertEquals(0, f.capacity.waiting());
    }

    @Test
    void aWaitThatSawAnOlderVersionReturnsAtOnce() throws Exception {
        api.place(body("a"));
        ObjectNode await = PlacementFixture.M.createObjectNode().put("seen", 0).put("maxWaitMs", 20_000);
        Response r = api.awaitCapacity(await).toCompletableFuture().get(1, TimeUnit.SECONDS);
        assertTrue(entity(r).path("changed").asBoolean());
    }

    @Test
    void aWaitWithNoChangeEndsAtItsLimit_savingTheCallerFromALostWakeUp() throws Exception {
        ObjectNode await = PlacementFixture.M.createObjectNode().put("seen", f.capacity.current()).put("maxWaitMs", 50);
        Response r = api.awaitCapacity(await).toCompletableFuture().get(2, TimeUnit.SECONDS);
        assertFalse(entity(r).path("changed").asBoolean());
    }

    // ---- bookkeeping after a call ----

    @Test
    void residencyOutcomesAndDemandAreAccepted() {
        assertEquals(204, api.residency(PlacementFixture.M.createObjectNode().put("server", SERVER)
                .put("model", MODEL).put("footprintMiB", 10_000)).getStatus());
        assertEquals(Map.of(MODEL, 10_000L), f.tickets.residentFootprintsFor(SERVER));

        ObjectNode outcome = PlacementFixture.M.createObjectNode().put("server", SERVER).put("model", MODEL)
                .put("durationMs", 1_200).put("ok", true);
        assertEquals(204, api.outcomes(outcome).getStatus());
        assertEquals(204, api.failures(PlacementFixture.M.createObjectNode().put("server", SERVER)).getStatus());

        ObjectNode started = PlacementFixture.identity("a").put("model", MODEL);
        assertEquals(204, api.callStarted("t", started).getStatus());
        assertEquals(204, api.waits(PlacementFixture.identity("a").put("model", MODEL).put("active", true))
                .getStatus());
    }

    @Test
    void aReplicaThatLostTheLeaseAnswersNotLeaderInsteadOfBusy() {
        f.table.fenceWith(() -> false);
        assertThrows(PlacementEngine.NotLeaderException.class, () -> api.place(body("a")));
        ObjectNode plan = body("planner", "t", MODEL, 10_000, 500, true);
        plan.putArray("candidates").add(MODEL);
        assertThrows(PlacementEngine.NotLeaderException.class, () -> api.plan("t", plan));
        assertThrows(PlacementEngine.NotLeaderException.class,
                () -> api.take("t", PlacementFixture.identity("planner")));
    }

    @Test
    void withNoServerStatePublishedNothingCanBePlaced() {
        f.state.data.clear();
        f.selector.invalidateCache();
        assertThrows(PlacementEngine.NoServerStateException.class, () -> api.place(body("a")));
    }

    @Test
    void theCapacityWaitIsBoundedEvenWhenTheCallerAsksForLonger() throws Exception {
        ObjectNode await = PlacementFixture.M.createObjectNode().put("seen", f.capacity.current())
                .put("maxWaitMs", Duration.ofHours(1).toMillis());
        var pending = api.awaitCapacity(await).toCompletableFuture();
        assertFalse(pending.isDone());
        f.capacity.bump();
        assertTrue(pending.get(1, TimeUnit.SECONDS).getStatus() == 200);
    }
}
