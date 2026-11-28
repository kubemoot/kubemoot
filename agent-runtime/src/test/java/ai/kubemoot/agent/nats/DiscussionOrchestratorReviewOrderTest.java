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
        await(() -> !publishedOfType("thread_close").isEmpty(), "the thread did not close: " + timeline);
        assertEquals(DiscussionOrchestrator.Phase.CLOSED, phase("t1"));
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
        assertTrue(ready.get(1).body().path("content").asText().contains("[FAILURE from compute] the program crashed"),
                "the domain analyst is shown that the generalist failed");
    }

    @Test
    void aGeneralistThatNeverRepliesExpiresAsAFailureAndTheSecondPassFollows() {
        var state = new DiscussionOrchestrator.ThreadState("t6");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        state.reviewSecondPass.set(List.of(ADVISOR));
        orchestrator.seedReviewRoster(state, List.of(COMPUTE), System.currentTimeMillis() - 200_000L);
        var advanced = new AtomicInteger();
        var cfg = new DiscussionOrchestrator.PhaseSettle(10, 0, false, false, () -> {
            advanced.incrementAndGet();
            state.reviewSecondPass.set(List.of());
            orchestrator.seedReviewRoster(state, List.of(ADVISOR), System.currentTimeMillis());
        });

        orchestrator.settlePhase(state, Instant.now(), 15, cfg);

        assertTrue(state.failureSignals.containsKey(COMPUTE), "the silent generalist is a failure, not a wait");
        assertEquals(1, advanced.get());
        assertEquals(Set.of(ADVISOR), state.phaseRoster.get().members());
    }

    private void ask(String t, String asker, String to) throws Exception {
        String q = "can you recheck that count?";
        send(t, asker, "question", q, "{\"signal\":\"question\",\"signalKind\":\"question\",\"signalTo\":\""
                + to + "\",\"signalStatement\":\"" + q + "\"}");
    }

    private String questionId(int index) {
        return publishedOfType("question_asked").get(index).body().path("metadata").path("questionId").asText();
    }

    private void check(String t) {
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now().plusSeconds(60));
    }

    @Test
    void generalistsFirst_aDomainAnalystsEarlySignalDoesNotEndPassOneAndItsFindingIsShownInPassTwo() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t8");

        send("t8", ADVISOR, AGREE, "early note", null);
        check("t8");
        assertEquals(1, publishedOfType(REVIEW_READY).size(), "a signal from outside the roster ends nothing");
        assertEquals(Set.of(COMPUTE), orchestrator.threadForTest("t8").phaseRoster.get().members());

        send("t8", COMPUTE, AGREE, "ok", null);
        settle("t8", REVIEW_TO_SYNTHESIS);
        var second = publishedOfType(REVIEW_READY).get(1);
        assertTrue(second.body().path("content").asText().contains("[" + ADVISOR + "] early note"),
                "the finding is on the table, so the summary shows it");
        assertFalse(DiscussionOrchestrator.rosterSignalled(orchestrator.threadForTest("t8")),
                "the earlier note is not the advisor's reply in its own pass");
    }

    @Test
    void generalistsFirst_aPassTwoAnalystMayAskAPassOneGeneralistToRecheck() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t9");
        send("t9", COMPUTE, AGREE, "11 rows", null);
        settle("t9", REVIEW_TO_SYNTHESIS);

        ask("t9", ADVISOR, COMPUTE);
        assertEquals(1, publishedOfType("question_asked").size());
        assertTrue(orchestrator.threadForTest("t9").pendingEvaluations.containsKey(COMPUTE));
        check("t9");
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, phase("t9"), "the question holds the phase");

        send("t9", COMPUTE, AGREE, "12 rows after recheck", "{\"answers\":\"" + questionId(0) + "\"}");
        send("t9", ADVISOR, AGREE, "12 checks out", null);
        settle("t9", REVIEW_TO_SYNTHESIS);
        await(() -> !publishedOfType("thread_close").isEmpty(), "the thread did not close: " + timeline);
    }

    @Test
    void generalistsFirst_aQuestionOpenAtThePassBoundaryHoldsPassTwo() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t10");

        ask("t10", COMPUTE, METRICS);
        check("t10");
        assertEquals(1, publishedOfType(REVIEW_READY).size(), "pass two waits while the question is open");

        send("t10", METRICS, AGREE, "12 rows, rechecked", "{\"answers\":\"" + questionId(0) + "\"}");
        send("t10", COMPUTE, AGREE, "ok", null);
        settle("t10", REVIEW_TO_SYNTHESIS);
        assertEquals(2, publishedOfType(REVIEW_READY).size());
    }

    @Test
    void generalistsFirst_aReopenedThreadStartsItsReviewAgain() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t11");
        send("t11", COMPUTE, AGREE, "ok", null);
        settle("t11", REVIEW_TO_SYNTHESIS);
        send("t11", ADVISOR, AGREE, "ok", null);
        settle("t11", REVIEW_TO_SYNTHESIS);
        await(() -> phase("t11") == DiscussionOrchestrator.Phase.CLOSED, "not closed: " + timeline);

        send("t11", "human", "reply", "And the pods?", null);

        var state = orchestrator.threadForTest("t11");
        assertEquals(DiscussionOrchestrator.Phase.EVALUATING, state.phase);
        assertTrue(state.reviewSecondPass.get().isEmpty());
        assertTrue(state.reviewFirstPass.get().isEmpty());
    }

    @Test
    void aRestartInPassOneStillRunsPassTwo() {
        var state = restartedThread("r1", Set.of(COMPUTE));

        String action = ResumeContinuation.run(orchestrator, state, new CoordinatorThreadState());

        assertEquals(ResumeContinuation.REPUBLISH_REVIEW_READY, action);
        assertEquals(List.of(ADVISOR), state.reviewSecondPass.get());
        assertEquals(List.of(COMPUTE), circle(publishedOfType(REVIEW_READY).get(0)));
        state.phaseRoster.get().recordTerminalSignal(COMPUTE);
        state.pendingEvaluations.clear();
        orchestrator.finishReviewPass(state);
        assertEquals(2, publishedOfType(REVIEW_READY).size());
        assertEquals(List.of(ADVISOR), circle(publishedOfType(REVIEW_READY).get(1)));
    }

    @Test
    void aRestartInPassTwoWakesTheDomainAnalystsAndGoesToSynthesisAfterThem() {
        var state = restartedThread("r2", Set.of(ADVISOR));

        String action = ResumeContinuation.run(orchestrator, state, new CoordinatorThreadState());

        assertEquals(ResumeContinuation.REPUBLISH_REVIEW_READY, action);
        assertTrue(state.reviewSecondPass.get().isEmpty(), "no third pass");
        assertEquals(List.of(COMPUTE), state.reviewFirstPass.get());
        assertEquals(List.of(ADVISOR), circle(publishedOfType(REVIEW_READY).get(0)));
        orchestrator.finishReviewPass(state);
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void aRestartWhoseRosterMatchesNeitherPassWarnsAndReviewsAsOnePass() {
        var state = restartedThread("r3", Set.of("some-other-analyst"));
        try (var capture = new ai.kubemoot.agent.logging.LogCapture()) {
            orchestrator.restoreReviewPasses(state);

            String logged = capture.text();
            assertTrue(logged.contains("Thread r3") && logged.contains("matches neither"), logged);
            assertTrue(logged.contains("[some-other-analyst]") && logged.contains("[compute]")
                    && logged.contains("[k8s-advisor]"), logged);
        }
        assertTrue(state.reviewSecondPass.get().isEmpty());
        assertTrue(state.reviewFirstPass.get().isEmpty());
    }

    @Test
    void aRestartWhoseRecomputedReviewHasNoSecondPassWarnsAndReviewsAsOnePass() throws Exception {
        resumes = """
                [{"name":"k8s-metrics","role":"tooler","domains":["kubernetes"]},
                 {"name":"k8s-workloads","role":"tooler","domains":["kubernetes"]},
                 {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]}]
                """;
        rebuild("generalistsFirst");
        var state = restartedThread("r4", Set.of(COMPUTE));
        try (var capture = new ai.kubemoot.agent.logging.LogCapture()) {
            orchestrator.restoreReviewPasses(state);

            String logged = capture.text();
            assertTrue(logged.contains("Thread r4") && logged.contains("no second pass"), logged);
            assertTrue(logged.contains("[compute]"), logged);
        }
        assertTrue(state.reviewSecondPass.get().isEmpty());
    }

    @Test
    void aRestartOfAReviewThatIsTogetherDoesNotWarn() throws Exception {
        rebuild("");
        var state = restartedThread("r5", Set.of(COMPUTE, ADVISOR));
        try (var capture = new ai.kubemoot.agent.logging.LogCapture()) {
            orchestrator.restoreReviewPasses(state);
            assertFalse(capture.text().contains("one pass after the restart"), capture.text());
        }
    }

    private DiscussionOrchestrator.ThreadState restartedThread(String id, Set<String> roster) {
        var state = new DiscussionOrchestrator.ThreadState(id);
        state.crew = "crew";
        state.userQuery = "How many rows?";
        state.advisoryTechnologies = new java.util.concurrent.CopyOnWriteArrayList<>(List.of("kubernetes"));
        state.agreeSignals.put(METRICS, "12 rows");
        state.agreeSignals.put(WORKLOADS, "11 pods");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        state.phaseRoster.set(PhaseRoster.of(roster));
        return state;
    }

    @Test
    void aTurnLimitFailureKeepsItsStatementAndCallLinesInAReview() {
        String call = "[CALL key=r9 tool=execute_code call=r9.call args=print(1) bytes=1]";
        var failure = new ai.kubemoot.agent.chat.ToolCallFailure(
                ai.kubemoot.agent.chat.ToolCallFailure.FailureType.ITERATIONS_EXHAUSTED, null, null, 10,
                "Did not finish within its turn limit of 10 tool-loop iterations; this is not a final answer.",
                "Unfinished text from its latest reply (not a final answer):\n" + "x".repeat(2000) + "\n\n" + call);

        String cut = DiscussionOrchestrator.cutKeepingCalls(failure.postedContent());

        assertTrue(cut.contains("not a final answer"), cut);
        assertTrue(cut.endsWith(call), cut);
    }

    @Test
    void aLongFindingKeepsItsCallLinesPastTheCut() {
        String call = "[CALL key=r9 tool=execute_code call=r9.call args=print(1) bytes=1]";
        String finding = "x".repeat(3000) + "\n" + call;

        String cut = DiscussionOrchestrator.cutKeepingCalls(finding);

        assertTrue(cut.endsWith(call), cut.substring(cut.length() - 80));
        assertTrue(cut.length() < 1200);
        assertEquals("short", DiscussionOrchestrator.cutKeepingCalls("short"));
        assertEquals(1003, DiscussionOrchestrator.cutKeepingCalls("y".repeat(3000)).length(),
                "a long finding with no call line is cut as before");
    }

    @Test
    void finishReviewPass_withNoSecondPassGoesToSynthesis() {
        var state = new DiscussionOrchestrator.ThreadState("t7");
        state.phase = DiscussionOrchestrator.Phase.REVIEW;
        orchestrator.finishReviewPass(state);
        assertEquals(DiscussionOrchestrator.Phase.SYNTHESIZING, state.phase);
    }

    @Test
    void theReviewShowsTheWorkingUnderAFindingAndProceedsWhenItCannotBeRead() throws Exception {
        rebuild("generalistsFirst");
        evaluate("t20");

        String first = publishedOfType(REVIEW_READY).get(0).body().path("content").asText();
        assertTrue(first.contains("[" + METRICS + "] 12 rows " + CALL_LINE + "\n[WORKING of r1]\n"
                + ReviewWorking.WORKING_UNAVAILABLE), first);
        assertTrue(first.contains("[OUTPUT of r1]\n" + ReviewWorking.OUTPUT_UNAVAILABLE), first);
        assertFalse(first.contains("[WORKING of 11 pods"), "a finding without calls shows nothing");
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, phase("t20"), "an unreadable working never blocks the review");
    }
}
