package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.when;

/**
 * The review phase in the order the archetype declares: with generalistsFirst the analysts that
 * declare no domain review first, and the analysts that declare a domain are woken once every
 * generalist has replied, with the generalists' findings in front of them.
 */
class DiscussionOrchestratorReviewOrderTest extends CoordinatorThreadTestBase {

    private static final String COMPUTE = "compute";
    private static final String ADVISOR = "k8s-advisor";
    private static final String METRICS = "k8s-metrics";
    private static final String WORKLOADS = "k8s-workloads";
    private static final String REVIEW_READY = "review_ready";
    private static final String AGREE = "agree";
    private static final String CALL_LINE = "[CALL key=r1 tool=execute_code call=r1.call args=print(2) bytes=2]";

    private volatile String order = "generalistsFirst";
    private volatile String resumes = """
            [{"name":"k8s-metrics","role":"tooler","domains":["kubernetes"]},
             {"name":"k8s-workloads","role":"tooler","domains":["kubernetes"]},
             {"name":"compute","role":"analyst"},
             {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]}]
            """;

    @Override
    protected boolean withBus() {
        return true;
    }

    @Override
    protected String resumesJson() {
        return resumes;
    }

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.reviewConvene()).thenReturn(Optional.of("domain"));
        when(discuss.reviewFallback()).thenReturn(Optional.of("primary"));
        when(discuss.reviewGeneralists()).thenReturn(Optional.of("always"));
        when(discuss.reviewOrder()).thenReturn(order.isEmpty() ? Optional.empty() : Optional.of(order));
    }

    private void rebuild(String reviewOrder) throws Exception {
        order = reviewOrder;
        build(true);
    }

    private void evaluate(String t) throws Exception {
        selection = SELECT_TWO;
        send(t, "human", "thread_start", "How many rows?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(t);
        send(t, METRICS, AGREE, "12 rows " + CALL_LINE, null);
        send(t, WORKLOADS, AGREE, "11 pods", null);
        settle(t, EVALUATION_TO_REVIEW);
    }

    private static List<String> circle(Published readyMessage) {
        var names = new ArrayList<String>();
        JsonNode circle = readyMessage.body().path("metadata").path("innerCircle");
        circle.forEach(n -> names.add(n.asText()));
        return names;
    }

    private DiscussionOrchestrator.Phase phase(String t) {
        return orchestrator.threadForTest(t).phase;
    }

    @Test
    void generalistsFirst_theGeneralistReviewsFirstAndTheDomainAnalystAfterItWithItsFinding() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t1");

        assertEquals(1, publishedOfType(REVIEW_READY).size(), "only the first pass is woken");
        assertEquals(List.of(COMPUTE), circle(publishedOfType(REVIEW_READY).get(0)));
        assertEquals(Set.of(COMPUTE), orchestrator.threadForTest("t1").phaseRoster.get().members());

        send("t1", COMPUTE, AGREE, "Eleven rows match " + CALL_LINE, null);
        settle("t1", REVIEW_TO_SYNTHESIS);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, phase("t1"), "the domain analyst has yet to reply");
        var ready = publishedOfType(REVIEW_READY);
        assertEquals(2, ready.size());
        assertEquals(List.of(ADVISOR), circle(ready.get(1)));
        String content = ready.get(1).body().path("content").asText();
        assertTrue(content.contains("[" + COMPUTE + "] Eleven rows match " + CALL_LINE), content);
        assertTrue(content.contains("[" + METRICS + "] 12 rows " + CALL_LINE), content);
        assertEquals(Set.of(ADVISOR), orchestrator.threadForTest("t1").phaseRoster.get().members());
        assertFalse(ready.get(0).messageId().equals(ready.get(1).messageId()), "a second wake, not a repeat");

        send("t1", ADVISOR, AGREE, "checked", null);
        settle("t1", REVIEW_TO_SYNTHESIS);
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING.ordinal() <= phase("t1").ordinal(), true);
        assertEquals(2, publishedOfType(REVIEW_READY).size(), "no third wake");
    }

    @Test
    void together_everyReviewerIsWokenAtOnce() throws Exception {
        rebuild("");
        evaluate("t2");

        var ready = publishedOfType(REVIEW_READY);
        assertEquals(1, ready.size());
        assertEquals(Set.of(COMPUTE, ADVISOR), Set.copyOf(circle(ready.get(0))));

        send("t2", COMPUTE, AGREE, "ok", null);
        send("t2", ADVISOR, AGREE, "ok", null);
        settle("t2", REVIEW_TO_SYNTHESIS);
        assertEquals(1, publishedOfType(REVIEW_READY).size());
        assertTrue(phase("t2").ordinal() > DiscussionOrchestrator.Phase.REVIEW.ordinal());
    }

    @Test
    void generalistsFirst_withNoGeneralistConvenedItIsOnePass() throws Exception {
        resumes = """
                [{"name":"k8s-metrics","role":"tooler","domains":["kubernetes"]},
                 {"name":"k8s-workloads","role":"tooler","domains":["kubernetes"]},
                 {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]}]
                """;
        rebuild("generalistsFirst");
        evaluate("t3");

        assertEquals(List.of(ADVISOR), circle(publishedOfType(REVIEW_READY).get(0)));
        send("t3", ADVISOR, AGREE, "ok", null);
        settle("t3", REVIEW_TO_SYNTHESIS);
        assertEquals(1, publishedOfType(REVIEW_READY).size());
        assertTrue(phase("t3").ordinal() > DiscussionOrchestrator.Phase.REVIEW.ordinal());
    }

    @Test
    void generalistsFirst_withNoDomainAnalystConvenedItIsOnePass() throws Exception {
        resumes = """
                [{"name":"k8s-metrics","role":"tooler","domains":["kubernetes"]},
                 {"name":"k8s-workloads","role":"tooler","domains":["kubernetes"]},
                 {"name":"compute","role":"analyst"}]
                """;
        rebuild("generalistsFirst");
        evaluate("t4");

        assertEquals(Set.of(COMPUTE), orchestrator.threadForTest("t4").phaseRoster.get().members());
        send("t4", COMPUTE, AGREE, "ok", null);
        settle("t4", REVIEW_TO_SYNTHESIS);
        assertEquals(1, publishedOfType(REVIEW_READY).size());
        assertTrue(phase("t4").ordinal() > DiscussionOrchestrator.Phase.REVIEW.ordinal());
    }

    @Test
    void generalistsFirst_aGeneralistThatFailsStillLetsTheDomainAnalystReview() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t5");

        send("t5", COMPUTE, "failure", "the program crashed", "{\"failureType\":\"tool_error\"}");
        settle("t5", REVIEW_TO_SYNTHESIS);

        assertEquals(DiscussionOrchestrator.Phase.REVIEW, phase("t5"));
        var ready = publishedOfType(REVIEW_READY);
        assertEquals(2, ready.size());
        assertEquals(List.of(ADVISOR), circle(ready.get(1)));
    }

    @Test
    void aGeneralistThatNeverRepliesExpiresAsAFailureAndTheSecondPassFollows() {
        var state = new DiscussionOrchestrator.ThreadState("t6");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        state.reviewSecondPass = List.of(ADVISOR);
        orchestrator.seedReviewRoster(state, List.of(COMPUTE), System.currentTimeMillis() - 200_000L);
        var advanced = new AtomicInteger();
        var cfg = new DiscussionOrchestrator.PhaseSettle(10, 0, false, false, () -> {
            advanced.incrementAndGet();
            state.reviewSecondPass = List.of();
            orchestrator.seedReviewRoster(state, List.of(ADVISOR), System.currentTimeMillis());
        });

        orchestrator.settlePhase(state, Instant.now(), 15, cfg);

        assertTrue(state.failureSignals.containsKey(COMPUTE), "the silent generalist is a failure, not a wait");
        assertEquals(1, advanced.get());
        assertEquals(Set.of(ADVISOR), state.phaseRoster.get().members());
    }

    @Test
    void finishReviewPass_withNoSecondPassGoesToSynthesis() {
        var state = new DiscussionOrchestrator.ThreadState("t7");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        orchestrator.finishReviewPass(state);
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }
}
