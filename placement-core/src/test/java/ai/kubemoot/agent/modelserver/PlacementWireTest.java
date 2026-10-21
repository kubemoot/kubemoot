package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;
import ai.kubemoot.agent.modelserver.PlacementWire.ModelNeed;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceReply;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The placement service's wire format: nothing a caller sends or reads is lost, and a bad request is refused. */
class PlacementWireTest {

    private static final ObjectMapper M = new ObjectMapper();
    private static final AgentIdentity ID = new AgentIdentity("crew-ns", "triager", "pilot");

    private static ServerState rig() {
        return new ServerState("rig0", "http://rig0:11434", 2, 0, 0, List.of("qwen3:14b"), true,
                "2026-10-20T00:00:00Z", 32_000, Map.of("qwen3:14b", 9_000L), Map.of(), 8_192, Map.of());
    }

    private static PlaceRequest request() {
        return new PlaceRequest(ID, "thread-1", "mulling", "learned",
                List.of(new ModelNeed("qwen3:14b", 9_000, 512), new ModelNeed("qwen3:8b", 5_000, 256)),
                1_200, false);
    }

    @Test
    void aRequestSurvivesTheWire() {
        PlaceRequest back = PlacementWire.requestFrom(M.valueToTree(PlacementWire.requestJson(M, request())));
        assertEquals(request(), back);
    }

    @Test
    void aRequestBecomesThePlacementRequest_preferredFirst() {
        var r = request().toPlacementRequest(List.of(rig()));
        assertEquals("qwen3:14b", r.preferred());
        assertEquals(List.of("qwen3:8b"), r.alternatives());
        assertEquals(9_000, r.footprint().applyAsLong("qwen3:14b"));
        assertEquals(256, r.kv().applyAsLong("qwen3:8b"));
        assertEquals(0, r.footprint().applyAsLong("unknown"));
        assertEquals(1_200, r.promptTokens());
        assertTrue(!r.mayUnload());
    }

    @Test
    void aPlacementRequestBecomesAWireRequest_andBack() {
        var original = request().toPlacementRequest(List.of(rig()));
        PlaceRequest wire = PlaceRequest.of(ID, "thread-1", "mulling", "learned", original);
        assertEquals(request(), wire);
    }

    @Test
    void aRequestNamingNoModelIsRefused() {
        var empty = M.createObjectNode();
        empty.putArray("models");
        assertThrows(IllegalArgumentException.class, () -> PlacementWire.requestFrom(empty));
        var blank = M.createObjectNode();
        blank.putArray("models").addObject().put("name", " ");
        assertThrows(IllegalArgumentException.class, () -> PlacementWire.requestFrom(blank));
    }

    @Test
    void aReplyKeepsTheTicketAndTheWholeExplanation() {
        var decision = new PlacementDecision(PlacementDecision.SHADOW, "warm, slot 1/2; learned policy agrees",
                List.of(new PlacementDecision.Alternative("rig1", "qwen3:14b", false, true,
                        new Prediction(0.0, 3.0, 0.5, 4.0)),
                        new PlacementDecision.Alternative("rig2", "qwen3:14b", true, false, null)),
                new Prediction(1.5, 0.0, 0.4, 3.9));
        var ticket = new Ticket("t-1", "rig0", 9_000, "placement-0", "2026-10-20T00:00:00Z", 512, "qwen3:14b");
        var reply = new PlaceReply("rig0", "http://rig0:11434", "qwen3:14b", 9_000, true, false,
                List.of("old-model"), ticket, decision, 3);

        PlaceReply back = PlacementWire.replyFrom(M.valueToTree(PlacementWire.replyJson(M, reply)));
        assertEquals(reply, back);
    }

    @Test
    void aReplyBecomesAPlacementCarryingTheServiceDecision() {
        var decision = new PlacementDecision(PlacementDecision.ALGORITHM, "warm, slot 1/2", List.of(),
                new Prediction(0.0, 0.0, 0.4, 3.9));
        var ticket = new Ticket("t-1", "rig0", 9_000, "placement-0", "2026-10-20T00:00:00Z", 512, "qwen3:14b");
        var reply = new PlaceReply("rig0", "http://rig0:11434", "qwen3:14b", 9_000, true, false, List.of(),
                ticket, decision, 1);

        CallPlanner.Placement p = reply.toPlacement(rig());
        assertEquals("qwen3:14b", p.model());
        assertEquals(9_000, p.footprintMiB());
        assertTrue(p.queued());
        assertEquals(ticket, p.pick().ticket());
        assertEquals(decision, p.decision(), "the receipt and prediction-error scoring read this decision");
        assertEquals("warm, slot 1/2", p.pick().score().reasoning());
    }

    @Test
    void aReplyWithoutPredictionOrDecisionReadsBackAsNone() {
        var ticket = new Ticket("t-1", "rig0", 1, "h", "c");
        var reply = new PlaceReply("rig0", "e", "m", 1, false, false, List.of(), ticket, null, 0);
        PlaceReply back = PlacementWire.replyFrom(M.valueToTree(PlacementWire.replyJson(M, reply)));
        assertNull(back.decision());

        var noPrediction = new PlacementDecision("algorithm", "r", List.of(), null);
        var withDecision = new PlaceReply("rig0", "e", "m", 1, false, false, List.of(), ticket, noPrediction, 0);
        PlaceReply back2 = PlacementWire.replyFrom(M.valueToTree(PlacementWire.replyJson(M, withDecision)));
        assertNull(back2.decision().predicted());
    }

    @Test
    void aRefusalSurvivesTheWire_andDefaultsToNoFit() {
        var r = new PlacementWire.Refusal("model-too-large", "no accelerator holds it");
        assertEquals(r, PlacementWire.refusalFrom(PlacementWire.refusalJson(M, r)));
        assertEquals(PlacementWire.NO_FIT, PlacementWire.refusalFrom(M.createObjectNode()).code());
    }
}
