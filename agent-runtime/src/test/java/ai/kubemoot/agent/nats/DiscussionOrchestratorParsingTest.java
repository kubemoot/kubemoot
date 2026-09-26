package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;

import java.util.concurrent.ConcurrentHashMap;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for the pure helpers extracted while reducing the cognitive complexity
 * of parseTriageResult (CC 22), handleWakeUpSignal (CC 18), and compactCatalog (CC 17):
 * stripCodeFence, parseSelectedAgent, shouldTrackWaking, and appendCatalogEntry.
 */
class DiscussionOrchestratorParsingTest {

    private static final ObjectMapper M = new ObjectMapper();

    private static JsonNode json(String s) {
        try {
            return M.readTree(s);
        } catch (Exception e) {
            throw new RuntimeException(e);
        }
    }

    // ---- stripCodeFence ----

    @Test
    void stripCodeFence_removesJsonFence() {
        assertEquals("{\"a\":1}", DiscussionOrchestrator.stripCodeFence("```json\n{\"a\":1}\n```"));
    }

    @Test
    void stripCodeFence_passesThroughPlainAndNullAndEmpty() {
        assertEquals("{\"a\":1}", DiscussionOrchestrator.stripCodeFence("  {\"a\":1}  "));
        assertEquals("", DiscussionOrchestrator.stripCodeFence(null));
        assertEquals("", DiscussionOrchestrator.stripCodeFence("   "));
    }

    // ---- parseSelectedAgent ----

    @Test
    void parseSelectedAgent_fullNode() {
        var a = DiscussionOrchestrator.parseSelectedAgent(
                json("{\"name\":\"k8s-tooler\",\"confidence\":0.8,\"reason\":\"pods\"}"));
        assertNotNull(a);
        assertEquals("k8s-tooler", a.name());
        assertEquals(0.8, a.confidence(), 0.0001);
        assertEquals("pods", a.reason());
    }

    @Test
    void parseSelectedAgent_defaultsConfidenceAndReason() {
        var a = DiscussionOrchestrator.parseSelectedAgent(json("{\"name\":\"gpu-tooler\"}"));
        assertNotNull(a);
        assertEquals(0.5, a.confidence(), 0.0001);
        assertEquals("", a.reason());
    }

    @Test
    void parseSelectedAgent_nullWhenNoName() {
        assertNull(DiscussionOrchestrator.parseSelectedAgent(json("{\"confidence\":0.9}")));
        assertNull(DiscussionOrchestrator.parseSelectedAgent(json("{\"name\":\"\"}")));
    }

    // ---- shouldTrackWaking ----

    @Test
    void shouldTrackWaking_onlyEvaluatingOrAdvisory() {
        var s = new DiscussionOrchestrator.ThreadState("t1");
        s.phase = Phase.EVALUATING;
        assertTrue(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
        s.phase = Phase.ADVISORY;
        assertTrue(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
        s.phase = Phase.REVIEW;
        assertFalse(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
        s.phase = Phase.CLOSED;
        assertFalse(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
        s.phase = Phase.PAUSED;
        assertFalse(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
        s.phase = Phase.SYNTHESIZING;
        assertFalse(DiscussionOrchestrator.shouldTrackWaking(s, "a"));
    }

    @Test
    void shouldTrackWaking_innerCircleScoping() {
        var s = new DiscussionOrchestrator.ThreadState("t2");
        s.phase = Phase.EVALUATING;
        s.innerCircle = ConcurrentHashMap.newKeySet();
        s.innerCircle.add("in");
        assertTrue(DiscussionOrchestrator.shouldTrackWaking(s, "in"), "member is tracked");
        assertFalse(DiscussionOrchestrator.shouldTrackWaking(s, "out"),
                "non-member excluded so the coordinator does not wait out its deadline");
    }

    @Test
    void shouldTrackWaking_emptyInnerCircleTracksAll() {
        var s = new DiscussionOrchestrator.ThreadState("t3");
        s.phase = Phase.EVALUATING;
        s.innerCircle = ConcurrentHashMap.newKeySet(); // empty -> no scoping yet
        assertTrue(DiscussionOrchestrator.shouldTrackWaking(s, "anyone"));
    }

    // ---- appendCatalogEntry ----

    @Test
    void appendCatalogEntry_fullEntry() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendCatalogEntry(sb, json(
                "{\"name\":\"k8s\",\"role\":\"analyst\",\"description\":\"cluster\",\"tools\":[\"get\",\"list\"]}"));
        assertEquals("- k8s [analyst]: cluster Tools: get, list.\n", sb.toString());
    }

    @Test
    void appendCatalogEntry_toolerRoleOmitted() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendCatalogEntry(sb, json(
                "{\"name\":\"gpu\",\"role\":\"tooler\",\"description\":\"gpus\"}"));
        assertEquals("- gpu: gpus\n", sb.toString(), "the default tooler role is not annotated");
    }

    @Test
    void appendCatalogEntry_nameOnly() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendCatalogEntry(sb, json("{\"name\":\"solo\"}"));
        assertEquals("- solo\n", sb.toString(),
                "no role/desc/tools -> bare name plus newline");
    }

    @Test
    void appendCatalogEntry_skipsNamelessNode() {
        var sb = new StringBuilder();
        DiscussionOrchestrator.appendCatalogEntry(sb, json("{\"role\":\"analyst\"}"));
        assertEquals("", sb.toString());
    }
}
