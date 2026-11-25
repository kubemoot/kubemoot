package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A moot where every agent failed for the same infrastructure reason ends at once, with an
 * answer that names the cause: no review round, no synthesis call that would fail the same
 * way, and no waiting out the phases' deadlines. The real orchestrator runs against a fake
 * model, a fake log and a fake bus.
 */
class CoordinatorInfrastructureFailureTest extends CoordinatorThreadTestBase {

    private static final String NO_OPERATOR = "Triage failed: server server-a could not make model-a ready: "
            + "no operator is listening for model lifecycle requests";
    private static final String ENGINE_REFUSED = "Mulling failed: server server-a could not make model-a ready: "
            + "the engine refused";

    @Override
    protected boolean withBus() {
        return true;
    }

    private void startAndEvaluate(String t) throws Exception {
        send(t, "human", "thread_start", "How many pods run?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
    }

    private void awaitAnswer(String t) throws InterruptedException {
        await(() -> !publishedOfType("synthesis").isEmpty() && !publishedOfType("thread_close").isEmpty(),
                "thread " + t + " did not close, got " + timeline);
    }

    @Test
    void everyAgentFailingBecauseNoOperatorListens_endsTheMootAndNamesTheCause() throws Exception {
        String t = "t-down";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "failure", NO_OPERATOR, "{\"failureType\":\"operator_unreachable\"}");
        send(t, "k8s-workloads", "failure", NO_OPERATOR, "{\"failureType\":\"operator_unreachable\"}");

        settle(t, EVALUATION_TO_REVIEW);
        awaitAnswer(t);

        var answer = publishedOfType("synthesis").get(0).body();
        assertEquals("No agent could answer: the crew's models could not be made ready (the operator did not respond).",
                answer.path("content").asText());
        assertEquals("operator_unreachable", answer.path("metadata").path("failureCause").asText());
        assertTrue(publishedOfType("review_ready").isEmpty(), "no review round for a crew that cannot run a model");
        assertEquals("CLOSED", orchestrator.threadForTest(t).phase.name());
    }

    @Test
    void failuresWithDifferentCauses_doNotEndTheMootEarly() throws Exception {
        String t = "t-mixed";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "failure", NO_OPERATOR, "{\"failureType\":\"operator_unreachable\"}");
        send(t, "k8s-workloads", "failure", ENGINE_REFUSED, "{\"failureType\":\"model_not_ready\"}");

        settle(t, EVALUATION_TO_REVIEW);
        await(() -> !publishedOfType("review_ready").isEmpty(), "the review did not start, got " + timeline);

        assertEquals("REVIEW", orchestrator.threadForTest(t).phase.name());
    }

    @Test
    void anAgentThatAgreed_keepsTheMootGoing() throws Exception {
        String t = "t-one-agrees";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "agree", "11 pods", null);
        send(t, "k8s-workloads", "failure", NO_OPERATOR, "{\"failureType\":\"operator_unreachable\"}");

        settle(t, EVALUATION_TO_REVIEW);
        await(() -> !publishedOfType("review_ready").isEmpty(), "the review did not start, got " + timeline);

        assertTrue(publishedOfType("synthesis").isEmpty() || !publishedOfType("synthesis").get(0).body()
                .path("content").asText().startsWith("No agent could answer"));
    }

    @Test
    void aFailureThatNamesNoCause_keepsTheMootGoing() throws Exception {
        String t = "t-unknown";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "failure", "Mulling failed: the tool returned an error", "{\"failureType\":\"model_timeout\"}");
        send(t, "k8s-workloads", "failure", "Mulling failed: the tool returned an error", "{\"failureType\":\"model_timeout\"}");

        settle(t, EVALUATION_TO_REVIEW);
        await(() -> !publishedOfType("review_ready").isEmpty(), "the review did not start, got " + timeline);

        assertFalse(publishedOfType("review_ready").isEmpty());
    }

    @Test
    void aFailureTextWithoutAPublishedType_keepsTheMootGoing() throws Exception {
        String t = "t-text-only";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "failure", NO_OPERATOR, null);
        send(t, "k8s-workloads", "failure", NO_OPERATOR, null);

        settle(t, EVALUATION_TO_REVIEW);
        await(() -> !publishedOfType("review_ready").isEmpty(), "the review did not start, got " + timeline);

        assertFalse(publishedOfType("review_ready").isEmpty(), "the coordinator reads the failureType, not the text");
    }

    @Test
    void oneFailureAndAStandAside_doNotSkipTheSynthesis() throws Exception {
        String t = "t-stand-aside";
        startAndEvaluate(t);
        send(t, "k8s-metrics", "failure", NO_OPERATOR, "{\"failureType\":\"operator_unreachable\"}");
        send(t, "k8s-workloads", "stand_aside", "", null);

        settle(t, EVALUATION_TO_REVIEW);
        await(() -> !publishedOfType("review_ready").isEmpty(), "the review did not start, got " + timeline);

        assertFalse(publishedOfType("review_ready").isEmpty(), "an agent that stood aside did not fail");
    }
}
