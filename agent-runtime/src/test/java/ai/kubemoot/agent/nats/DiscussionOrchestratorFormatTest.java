package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Tests for the DiscussionOrchestrator thread-label, array-field, and advisory
 * summary helpers. Plain JUnit 5.
 */
class DiscussionOrchestratorFormatTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void fixedLabels() {
        assertEquals("[USER QUESTION]", DiscussionOrchestrator.threadLabel("thread_start", "human"));
        assertEquals("[ADVISORY READY]", DiscussionOrchestrator.threadLabel("advisory_ready", "c"));
        assertEquals("[REVIEW READY]", DiscussionOrchestrator.threadLabel("review_ready", "c"));
        assertEquals("[USER REPLY]", DiscussionOrchestrator.threadLabel("reply", "human"));
        assertEquals("[FACILITATOR FOLLOW-UP]", DiscussionOrchestrator.threadLabel("follow_up", "c"));
        assertEquals("[SYNTHESIS]", DiscussionOrchestrator.threadLabel("synthesis", "c"));
    }

    @Test
    void authoredLabels() {
        assertEquals("[ADVISORY from c]", DiscussionOrchestrator.threadLabel("advisory", "c"));
        assertEquals("[RESPONSE from a]", DiscussionOrchestrator.threadLabel("agree", "a"));
        assertEquals("[RESPONSE from a]", DiscussionOrchestrator.threadLabel("contribution", "a"));
        assertEquals("[CONCERN from a]", DiscussionOrchestrator.threadLabel("concern", "a"));
        assertEquals("[BLOCK from a]", DiscussionOrchestrator.threadLabel("block", "a"));
        assertEquals("[STAND ASIDE from a]", DiscussionOrchestrator.threadLabel("stand_aside", "a"));
        assertEquals("[STAND ASIDE from a]", DiscussionOrchestrator.threadLabel("decline", "a"));
        assertEquals("[PROPOSAL from a]", DiscussionOrchestrator.threadLabel("proposal", "a"));
    }

    @Test
    void unknownTypeIsUpperCasedWithTheAuthor() {
        assertEquals("[WAKING from a]", DiscussionOrchestrator.threadLabel("waking", "a"));
    }

    @Test
    void textsReadsEveryElementAndToleratesMissingOrNonArrayFields() throws Exception {
        var node = MAPPER.readTree("{\"technologies\":[\"k8s\",\" \",\"nats\"],\"brief\":\"x\"}");
        assertEquals(List.of("k8s", " ", "nats"), DiscussionOrchestrator.texts(node, "technologies"));
        assertTrue(DiscussionOrchestrator.texts(node, "brief").isEmpty());
        assertTrue(DiscussionOrchestrator.texts(node, "missing").isEmpty());
    }

    @Test
    void nonBlankTextsTrimsAndDropsBlanks() throws Exception {
        var node = MAPPER.readTree("{\"selected\":[\" a \",\"\",\"  \",\"b\"]}");
        assertEquals(List.of("a", "b"), DiscussionOrchestrator.nonBlankTexts(node, "selected"));
        assertTrue(DiscussionOrchestrator.nonBlankTexts(node, "missing").isEmpty());
    }

    @Test
    void advisorySummaryNamesTechnologiesWhenPresent() {
        assertEquals("Technologies: k8s, nats. brief", DiscussionOrchestrator.advisorySummary(List.of("k8s", "nats"), "brief"));
        assertEquals("brief", DiscussionOrchestrator.advisorySummary(List.of(), "brief"));
    }
}
