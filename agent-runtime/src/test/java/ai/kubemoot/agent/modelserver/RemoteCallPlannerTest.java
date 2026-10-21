package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.FakePlacementServer.Reply;
import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceReply;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

/** The remote placement: the same {@link CallPlacement} the caller uses in-process, answered by the service. */
class RemoteCallPlannerTest {

    private static final ObjectMapper M = new ObjectMapper();
    private static final AgentIdentity ID = new AgentIdentity("crew-ns", "triager", "pilot");
    private static final ServerState RIG = new ServerState("rig0", "http://rig0:11434", 2, 0, 0,
            List.of("qwen3:14b"), true, "2026-10-20T00:00:00Z", 32_000, Map.of("qwen3:14b", 9_000L));

    private FakePlacementServer server;
    private ServerSelector selector;
    private RemoteCallPlanner planner;
    private SimpleMeterRegistry registry;

    @BeforeEach
    void setUp() throws Exception {
        server = new FakePlacementServer();
        selector = mock(ServerSelector.class);
        when(selector.readState()).thenReturn(List.of(RIG));
        registry = new SimpleMeterRegistry();
        planner = new RemoteCallPlanner(new PlacementClient(server.url(), M, Duration.ofMillis(500),
                Duration.ofSeconds(2)), selector, ID, "shadow", M, registry);
    }

    @AfterEach
    void tearDown() {
        server.close();
    }

    private static CallPlanner.PlacementRequest request() {
        return new CallPlanner.PlacementRequest("qwen3:14b", List.of(), List.of(RIG), m -> 9_000L, m -> 256L, 500, true);
    }

    private static String reply(String serverName) {
        var decision = new PlacementDecision("shadow", "warm, slot 1/2", List.of(), new Prediction(0, 0, 0.4, 3.9));
        var ticket = new Ticket("t-1", serverName, 9_000, "placement-0", "2026-10-20T00:00:00Z", 256, "qwen3:14b");
        return PlacementWire.replyJson(M, new PlaceReply(serverName, "http://x", "qwen3:14b", 9_000, false, false,
                List.of(), ticket, decision, 3)).toString();
    }

    @Test
    void aCallIsPlacedWhereTheServiceSays_withItsTicketAndExplanation() {
        server.on("POST", "/placement/v1/place", Reply.ok(reply("rig0")));

        var placement = planner.place(request()).orElseThrow();

        assertEquals("rig0", placement.pick().server().name());
        assertEquals("t-1", placement.pick().ticket().ticketId());
        assertEquals("shadow", placement.decision().policy(), "receipts and prediction-error scoring read this");
        assertEquals(3.9, placement.decision().predicted().generate());
        assertEquals(Optional.of("rig0"), planner.lastKnownServer("qwen3:14b"));
        assertEquals(3, planner.activeCountFor("rig0"));
        var sent = server.seen("POST", "/placement/v1/place").get(0).body();
        assertEquals("shadow", sent.path("policy").asText());
        assertEquals("triager", sent.path("agent").asText());
        assertEquals(9_000, sent.path("models").path(0).path("footprintMiB").asInt());
    }

    @Test
    void noRoomIsEmpty_asItIsInProcess() {
        server.on("POST", "/placement/v1/place", Reply.json(409, "{\"code\":\"kubemoot_no_fit\",\"reason\":\"busy\"}"));
        assertTrue(planner.place(request()).isEmpty());
    }

    @Test
    void anUnreachableServiceThrowsUnavailable_andIsCounted() {
        server.on("POST", "/placement/v1/place", Reply.json(503, "{\"code\":\"not_leader\"}"));
        assertThrows(PlacementUnavailableException.class, () -> planner.place(request()));
        assertEquals(1.0, registry.counter("kubemoot_placement_client_unavailable_total", "op", "place").count());
    }

    @Test
    void aServerTheAgentDoesNotSeeGivesTheTicketBack_andFallsBack() throws Exception {
        server.on("POST", "/placement/v1/place", Reply.ok(reply("rig9")));
        server.on("POST", "/placement/v1/release", Reply.noContent());

        assertThrows(PlacementUnavailableException.class, () -> planner.place(request()));
        assertEquals("t-1", server.seen("POST", "/placement/v1/release").get(0).body().path("ticket").asText());
    }

    @Test
    void aPlanIsTakenOnlyForThreadsThisAgentPlanned() {
        server.on("POST", "/placement/v1/threads/t1/plan", Reply.json(202, ""));
        server.on("POST", "/placement/v1/threads/t1/take", Reply.ok(reply("rig0")));

        assertTrue(planner.takeHeld("t1").isEmpty(), "no plan, so no request");
        assertTrue(server.seen("POST", "/placement/v1/threads/t1/take").isEmpty());

        planner.commitPlan("t1", 1234L, List.of("qwen3:14b"), () -> Optional.of(request()));
        var plan = server.seen("POST", "/placement/v1/threads/t1/plan").get(0).body();
        assertEquals(1234L, plan.path("selectedAtMs").asLong());
        assertEquals("qwen3:14b", plan.path("candidates").path(0).asText());

        assertEquals("t-1", planner.takeHeld("t1").orElseThrow().pick().ticket().ticketId());
        assertTrue(planner.takeHeld("t1").isEmpty(), "handed over once");
    }

    @Test
    void whenTheServiceCannotPlanOrTake_theCallIsPlacedWhenItIsMade() {
        server.on("POST", "/placement/v1/threads/t2/plan", Reply.json(503, "{}"));
        planner.commitPlan("t2", 1L, List.of("qwen3:14b"), () -> Optional.of(request()));
        assertTrue(planner.takeHeld("t2").isEmpty());
    }

    @Test
    void releasingATicketReachesTheService_andAnOutageNeverFailsTheCall() {
        server.on("POST", "/placement/v1/release", Reply.noContent());
        var ticket = new Ticket("t-9", "rig0", 1, "h", "c");
        planner.releaseTicket(ticket);
        assertEquals("t-9", server.seen("POST", "/placement/v1/release").get(0).body().path("ticket").asText());

        server.close();
        planner.releaseTicket(ticket);
        planner.releaseTicket(null);
    }

    @Test
    void reportsGoOutOffTheCallsPath() throws Exception {
        server.on("POST", "/placement/v1/outcomes", Reply.noContent());
        server.on("POST", "/placement/v1/residency", Reply.noContent());
        server.on("DELETE", "/placement/v1/threads/t3", Reply.noContent());
        var done = server.expect(3);

        planner.recordOutcome("rig0", "qwen3:14b", 1_200, true);
        planner.recordResidency("rig0", "qwen3:14b", 9_000);
        planner.release("t3");

        assertTrue(FakePlacementServer.await(done));
        assertEquals(1_200, server.seen("POST", "/placement/v1/outcomes").get(0).body().path("durationMs").asInt());
        assertTrue(server.seen("DELETE", "/placement/v1/threads/t3").get(0).pathAndQuery()
                .contains("agent=triager"));
    }

    @Test
    void theModeIsTheConfiguredPolicy() {
        assertEquals(PlacementMode.SHADOW, planner.mode());
    }
}
