package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.ReviewDecision.Evidence;
import ai.kubemoot.agent.nats.ReviewDecision.Shape;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

class ReviewDecisionTest {

    // ---- guards: failure, concern, objection, or no agreement always escalate ----

    @Test
    void cleanAgreement_isLeftToTheCrewsPolicy() {
        assertTrue(ReviewDecision.forced(new Evidence(1, 0, 0, 0, 0)).isEmpty());
        assertTrue(ReviewDecision.forced(new Evidence(3, 0, 0, 0, 0)).isEmpty());
    }

    @Test
    void aFailedTooler_forcesTheFullReview() {
        var d = ReviewDecision.forced(new Evidence(2, 1, 0, 0, 0)).orElseThrow();
        assertEquals(Shape.FULL, d.shape());
        assertTrue(d.forced());
    }

    @Test
    void aConcernOrABlock_forcesTheFullReview() {
        assertEquals(Shape.FULL, ReviewDecision.forced(new Evidence(1, 0, 1, 0, 0)).orElseThrow().shape());
        assertEquals(Shape.FULL, ReviewDecision.forced(new Evidence(1, 0, 0, 1, 0)).orElseThrow().shape());
    }

    @Test
    void noToolerAgreement_forcesTheFullReview() {
        assertEquals(Shape.FULL, ReviewDecision.forced(new Evidence(0, 0, 0, 0, 0)).orElseThrow().shape());
    }

    // ---- parsing the decision call's answer ----

    @Test
    void parse_readsEachShape() {
        assertEquals(Shape.CONCUR, ReviewDecision.parse("{\"review\":\"concur\",\"reason\":\"data answers it\"}").shape());
        assertEquals(Shape.FULL, ReviewDecision.parse("{\"review\":\"full\",\"reason\":\"judgment\"}").shape());
        assertEquals(Shape.NONE, ReviewDecision.parse("{\"review\":\"none\",\"reason\":\"policy\"}").shape());
    }

    @Test
    void parse_keepsTheReason_andIsNotForced() {
        var d = ReviewDecision.parse("{\"review\":\"concur\",\"reason\":\"the listing answers it\"}");
        assertEquals("the listing answers it", d.reason());
        assertFalse(d.forced());
    }

    @Test
    void parse_toleratesCaseReasoningBlocksFencesAndProse() {
        assertEquals(Shape.CONCUR, ReviewDecision.parse(" {\"review\": \" CONCUR \"} ").shape());
        assertEquals(Shape.CONCUR, ReviewDecision.parse(
                "<think>the data {maybe} answers it</think>\n{\"review\":\"concur\",\"reason\":\"r\"}").shape());
        assertEquals(Shape.FULL, ReviewDecision.parse("```json\n{\"review\":\"full\"}\n```").shape());
        assertEquals(Shape.CONCUR, ReviewDecision.parse("Decision: {\"review\":\"concur\"} done").shape());
    }

    @Test
    void parse_anythingUnreadable_isAForcedFullReview() {
        for (String bad : new String[] {null, "", "concur", "{not json}", "{\"review\":\"maybe\"}",
                "{\"reason\":\"no shape\"}", "<think>{\"review\":\"concur\"}</think>"}) {
            var d = ReviewDecision.parse(bad);
            assertEquals(Shape.FULL, d.shape(), "unreadable: " + bad);
            assertTrue(d.forced(), "unreadable answers are a runtime guard, not the crew's choice: " + bad);
        }
    }

    @Test
    void jsonObjectIn_findsTheOutermostObject() {
        assertEquals("{\"a\":{\"b\":1}}", ReviewDecision.jsonObjectIn("x {\"a\":{\"b\":1}} y"));
        assertNull(ReviewDecision.jsonObjectIn("no object"));
        assertNull(ReviewDecision.jsonObjectIn("} backwards {"));
        assertNull(ReviewDecision.jsonObjectIn(null));
    }

    // ---- the decision call's user message is data only ----

    @Test
    void prompt_carriesTheQuestionCountsAnalystsResultsAndShape() {
        String p = ReviewDecision.prompt("List the namespaces", "Discussion signals:\n- Tooler agrees: 1\n",
                List.of("k8s-advisor"), "[k8s-config] default, kube-system\n");
        assertTrue(p.contains("User question: \"List the namespaces\""));
        assertTrue(p.contains("- Tooler agrees: 1"), "the runtime's counts, never the model's");
        assertTrue(p.contains("Analysts selected for this question: k8s-advisor"));
        assertTrue(p.contains("[k8s-config] default, kube-system"));
        assertTrue(p.contains("\"review\": \"concur\" | \"full\" | \"none\""));
    }

    @Test
    void prompt_namesNoAnalystsWhenNoneWasSelected() {
        String p = ReviewDecision.prompt("q", "Discussion signals:\n", List.of(), "");
        assertTrue(p.contains("Analysts selected for this question: none"));
    }

    @Test
    void anOversizedResult_forcesTheFullReview() {
        var d = ReviewDecision.forced(new Evidence(2, 0, 0, 0, 1)).orElseThrow();
        assertEquals(Shape.FULL, d.shape());
        assertTrue(d.forced());
        assertTrue(d.reason().contains("larger than a concurrence check reads"), d.reason());
    }

    @Test
    void aFailure_isNamedBeforeAnOversizedResult() {
        assertEquals("a tooler failed", ReviewDecision.forced(new Evidence(2, 1, 0, 0, 1)).orElseThrow().reason());
    }

    @Test
    void concurrenceRequest_namesTheConcernSentinel() {
        assertTrue(ReviewDecision.CONCURRENCE_REQUEST.contains(DiscussionSubscriber.CONCERN_SENTINEL),
                "the request tells the analyst how to raise a concern the subscriber recognizes");
    }

    @Test
    void concurrenceRequest_namesTheConcurSentinel_andAsksForAVerdict() {
        assertTrue(ReviewDecision.CONCURRENCE_REQUEST.contains(ai.kubemoot.agent.util.ReplySentinels.CONCUR),
                "the request tells the analyst how to concur in a way the subscriber recognizes");
        assertTrue(ReviewDecision.CONCURRENCE_REQUEST.contains("verdict, not a new answer"));
    }
}
