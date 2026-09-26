package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Tests for conversation history serialization and turn management.
 * Uses plain JUnit (no @QuarkusTest) — CI-friendly without Ollama or NATS.
 *
 * Exercises serializeHistory / deserializeHistory which use manual JsonNode
 * navigation rather than Jackson bean/record reflection — the latter fails
 * silently under GraalVM native and causes multi-turn history loss.
 */
class ConversationHistoryTest {

    @Test
    void roundTripPreservesAllFields() throws Exception {
        var history = new DiscussionOrchestrator.ConversationHistory();
        history.addTurn(new DiscussionOrchestrator.ConversationTurn(
                "What pods are running?", "Here are the pods: ...", "thread-1", "2026-03-21T10:00:00Z"), 10);
        history.addTurn(new DiscussionOrchestrator.ConversationTurn(
                "Which uses most memory?", "The pod with most memory is ...", "thread-2", "2026-03-21T10:05:00Z"), 10);

        byte[] json = DiscussionOrchestrator.serializeHistory(history);
        var restored = DiscussionOrchestrator.deserializeHistory(json);

        assertEquals(2, restored.getTurns().size());
        assertEquals("What pods are running?", restored.getTurns().get(0).query());
        assertEquals("Here are the pods: ...", restored.getTurns().get(0).response());
        assertEquals("thread-1", restored.getTurns().get(0).threadId());
        assertEquals("2026-03-21T10:00:00Z", restored.getTurns().get(0).timestamp());
        assertEquals("Which uses most memory?", restored.getTurns().get(1).query());
        assertEquals("thread-2", restored.getTurns().get(1).threadId());
    }

    @Test
    void maxTurnsEvictsOldest() {
        var history = new DiscussionOrchestrator.ConversationHistory();
        for (int i = 1; i <= 12; i++) {
            history.addTurn(new DiscussionOrchestrator.ConversationTurn(
                    "Q" + i, "A" + i, "thread-" + i, "2026-03-21T10:0" + i + ":00Z"), 10);
        }

        assertEquals(10, history.getTurns().size());
        // Oldest two (Q1, Q2) should be evicted
        assertEquals("Q3", history.getTurns().get(0).query());
        assertEquals("Q12", history.getTurns().get(9).query());
    }

    @Test
    void emptyHistoryRoundtrips() throws Exception {
        var history = new DiscussionOrchestrator.ConversationHistory();

        byte[] json = DiscussionOrchestrator.serializeHistory(history);
        var restored = DiscussionOrchestrator.deserializeHistory(json);

        assertNotNull(restored.getTurns());
        assertTrue(restored.getTurns().isEmpty());
    }

    @Test
    void deserializeToleratesMissingFields() throws Exception {
        // KV value with missing optional fields — defensive: must not throw,
        // missing fields default to empty strings (or now() for lastAccessed)
        String partial = "{\"turns\":[{\"query\":\"hello\"}]}";

        var restored = DiscussionOrchestrator.deserializeHistory(partial.getBytes(StandardCharsets.UTF_8));

        assertEquals(1, restored.getTurns().size());
        assertEquals("hello", restored.getTurns().get(0).query());
        assertEquals("", restored.getTurns().get(0).response());
        assertEquals("", restored.getTurns().get(0).threadId());
        assertEquals("", restored.getTurns().get(0).timestamp());
    }

    @Test
    void deserializeToleratesEmptyObject() throws Exception {
        // Defensive: completely empty JSON yields empty history rather than NPE
        var restored = DiscussionOrchestrator.deserializeHistory("{}".getBytes(StandardCharsets.UTF_8));

        assertNotNull(restored.getTurns());
        assertTrue(restored.getTurns().isEmpty());
    }

    @Test
    void deserializeToleratesMalformedLastAccessed() throws Exception {
        // A garbage lastAccessed must not break the deserialize — fallback to now()
        String bad = "{\"lastAccessed\":\"not-an-instant\",\"turns\":[]}";

        var restored = DiscussionOrchestrator.deserializeHistory(bad.getBytes(StandardCharsets.UTF_8));

        assertNotNull(restored.lastAccessed);
        assertTrue(restored.getTurns().isEmpty());
    }

    @Test
    void serializedJsonHasExpectedShape() throws Exception {
        var history = new DiscussionOrchestrator.ConversationHistory();
        history.addTurn(new DiscussionOrchestrator.ConversationTurn(
                "q1", "a1", "t1", "2026-03-21T10:00:00Z"), 10);

        byte[] json = DiscussionOrchestrator.serializeHistory(history);
        String text = new String(json, StandardCharsets.UTF_8);

        // Top-level keys present
        assertTrue(text.contains("\"lastAccessed\""), "missing lastAccessed key");
        assertTrue(text.contains("\"turns\""), "missing turns key");
        // Turn fields present (proves we're not emitting an empty bean)
        assertTrue(text.contains("\"query\":\"q1\""), "missing query field — Jackson bean serialization regression?");
        assertTrue(text.contains("\"response\":\"a1\""), "missing response field");
        assertTrue(text.contains("\"threadId\":\"t1\""), "missing threadId field");
        assertTrue(text.contains("\"timestamp\":\"2026-03-21T10:00:00Z\""), "missing timestamp field");
    }
}
