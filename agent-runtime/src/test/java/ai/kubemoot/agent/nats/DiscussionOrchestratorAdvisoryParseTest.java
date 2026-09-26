package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.LegacyAdvisory;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for parseLegacyAdvisory, extracted from the (formerly CC-46)
 * transitionToEvaluating fallback path. Pins the JSON-parsing tolerance: clean
 * JSON, a ```json code fence, missing fields, invalid JSON (raw-text fallback),
 * and null/empty input.
 */
class DiscussionOrchestratorAdvisoryParseTest {

    @Test
    void parsesCleanJson() {
        LegacyAdvisory a = DiscussionOrchestrator.parseLegacyAdvisory(
                "{\"technologies\":[\"k8s\",\"gpu\"],\"layers\":[\"compute\"],\"wisdom\":\"check the nodes\"}");
        assertEquals(java.util.List.of("k8s", "gpu"), a.technologies());
        assertEquals(java.util.List.of("compute"), a.layers());
        assertEquals("check the nodes", a.wisdom());
    }

    @Test
    void stripsCodeFence() {
        LegacyAdvisory a = DiscussionOrchestrator.parseLegacyAdvisory(
                "```json\n{\"technologies\":[\"nats\"],\"wisdom\":\"streams\"}\n```");
        assertEquals(java.util.List.of("nats"), a.technologies());
        assertEquals("streams", a.wisdom());
        assertTrue(a.layers().isEmpty(), "no layers field -> empty");
    }

    @Test
    void missingFieldsDefaultEmpty() {
        LegacyAdvisory a = DiscussionOrchestrator.parseLegacyAdvisory("{\"wisdom\":\"only wisdom\"}");
        assertTrue(a.technologies().isEmpty());
        assertTrue(a.layers().isEmpty());
        assertEquals("only wisdom", a.wisdom());
    }

    @Test
    void invalidJsonFallsBackToRawText() {
        String raw = "the coordinator could not produce JSON, just prose";
        LegacyAdvisory a = DiscussionOrchestrator.parseLegacyAdvisory(raw);
        assertTrue(a.technologies().isEmpty(), "no technologies from non-JSON");
        assertTrue(a.layers().isEmpty());
        assertEquals(raw, a.wisdom(), "raw text is preserved as wisdom");
    }

    @Test
    void nullAndEmptyAreSafe() {
        LegacyAdvisory n = DiscussionOrchestrator.parseLegacyAdvisory(null);
        assertEquals("", n.wisdom());
        assertTrue(n.technologies().isEmpty());

        LegacyAdvisory e = DiscussionOrchestrator.parseLegacyAdvisory("");
        assertEquals("", e.wisdom());
        assertTrue(e.layers().isEmpty());
    }
}
