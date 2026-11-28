package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.CopyOnWriteArrayList;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.when;

/**
 * A whole thread through the clarifying-question round: an analyst doubts a tooler's finding and
 * asks it to recheck; the coordinator relays the question; the tooler re-runs and its revised
 * finding replaces the earlier one; the analyst is woken with it and signals again; the phase
 * settles on state, never on a clock.
 */
class RecheckThreadTest extends CoordinatorThreadTestBase {

    private static final String THREAD = "t-recheck";
    private static final String METRICS = "k8s-metrics";
    private static final String WORKLOADS = "k8s-workloads";
    private static final String ANALYST = "k8s-analyst";
    private static final String WRONG = "the cluster has 100 nodes";
    private static final String REVISED = "the cluster has 7 nodes; 100 was the pod count";
    private static final String QUESTION = "can you recheck the node count? 100 seems high for this homelab";

    private volatile int maxRounds = 1;
    private final List<String> synthesisPrompts = new CopyOnWriteArrayList<>();

    @Override
    protected boolean withBus() {
        return true;
    }

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.recheckMaxRounds()).thenAnswer(call -> Optional.of(maxRounds));
        when(chatService.simpleLlmCallWithTokens(any(), anyString())).thenAnswer(call -> {
            String prompt = call.getArgument(1);
            if (prompt.contains("Discussion thread")) {
                synthesisPrompts.add(prompt);
                return new ChatService.SimpleLlmResult("Seven nodes.", 100, 20);
            }
            return new ChatService.SimpleLlmResult(selection, 100, 20);
        });
    }

    private DiscussionOrchestrator.ThreadState state() {
        return orchestrator.threadForTest(THREAD);
    }

    /** Evidence from the two toolers, then REVIEW, where the analyst is the roster. */
    private void toReview(String metricsFinding) throws Exception {
        send(THREAD, "human", "thread_start", "How many nodes does the cluster have?",
                "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(THREAD);
        send(THREAD, METRICS, "agree", metricsFinding, null);
        send(THREAD, WORKLOADS, "agree", "no workload is pending", null);
        settle(THREAD, EVALUATION_TO_REVIEW);
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state().phase);
    }

    private void ask(String asker, String to) throws Exception {
        send(THREAD, asker, "question", QUESTION, "{\"signal\":\"question\",\"signalKind\":\"question\","
                + "\"signalTo\":\"" + to + "\",\"signalStatement\":\"" + QUESTION + "\"}");
    }

    private String relayedQuestionId() {
        return publishedOfType("question_asked").get(0).body().path("metadata").path("questionId").asText();
    }

    private void answer(String agent, String type, String content) throws Exception {
        send(THREAD, agent, type, content, "{\"answers\":\"" + relayedQuestionId() + "\"}");
    }

    private void checkWithoutSettling() {
        orchestrator.checkThreadTransition(state(), Instant.now().plusSeconds(60));
    }

    private List<CoordinatorEvent> events(String type) {
        return forThread(THREAD).stream().filter(e -> e.type().equals(type)).toList();
    }

    @Test
    void theQuestionIsRelayedTheFindingRevisedAndTheAskerSignalsAgain() throws Exception {
        toReview(WRONG);

        ask(ANALYST, METRICS);

        JsonNode relayed = publishedOfType("question_asked").get(0).body();
        assertEquals(List.of(METRICS), MAPPER.convertValue(relayed.path("metadata").path("innerCircle"), List.class),
                "only the agent asked wakes");
        assertEquals(ANALYST, relayed.path("metadata").path("askedBy").asText());
        assertTrue(relayed.path("content").asText().contains(QUESTION));
        assertTrue(state().pendingEvaluations.containsKey(METRICS), "the agent asked is pending");
        assertFalse(state().pendingEvaluations.containsKey(ANALYST), "the asker waits; its run ended");
        assertFalse(state().phaseRoster.get().signalled().contains(ANALYST), "a question does not end the turn");
        checkWithoutSettling();
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state().phase, "the phase holds while the question is open");
        assertEquals(1, events("question_asked").size());
        agreesNow(THREAD, "the log and the live state agree after the question");

        answer(METRICS, "agree", REVISED);

        assertEquals(REVISED, state().agreeSignals.get(METRICS), "the revised finding replaces the earlier one");
        JsonNode woken = publishedOfType("question_answered").get(0).body();
        assertEquals(List.of(ANALYST), MAPPER.convertValue(woken.path("metadata").path("innerCircle"), List.class));
        assertTrue(woken.path("content").asText().contains(REVISED));
        assertTrue(state().pendingEvaluations.containsKey(ANALYST), "the asker is pending again");
        checkWithoutSettling();
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state().phase, "the phase waits for the asker");
        agreesNow(THREAD, "the log and the live state agree after the answer");

        send(THREAD, ANALYST, "agree", "7 nodes checks out", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);

        await(() -> !synthesisPrompts.isEmpty(), "no synthesis ran: " + timeline);
        String prompt = synthesisPrompts.get(0);
        assertTrue(prompt.contains("[EARLIER FINDING from k8s-metrics, replaced by its recheck below]"), prompt);
        assertFalse(prompt.contains(WRONG), "the replaced finding's text is not shown to the synthesis");
        assertTrue(prompt.contains(REVISED));
        assertTrue(prompt.contains("[QUESTION from k8s-analyst]"), "the synthesis sees the question");
        assertTrue(prompt.contains("Clarifying questions: 1"), prompt);
        assertEquals(1, events("question_answered").size());
        BusContractTest.assertMessageConforms(relayed);
        BusContractTest.assertMessageConforms(woken);
    }

    @Test
    void aRevisedFindingMayChangeTheVerdict() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        answer(METRICS, "concern", "my first count was wrong: " + REVISED);

        assertFalse(state().agreeSignals.containsKey(METRICS), "the earlier agree is gone");
        assertTrue(state().concernSignals.get(METRICS).contains(REVISED));
        agreesNow(THREAD, "the reducer drops the earlier verdict too");
    }

    @Test
    void pastTheRoundLimitAQuestionIsAConcern() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);
        answer(METRICS, "agree", REVISED);

        ask(ANALYST, WORKLOADS);

        assertEquals(1, publishedOfType("question_asked").size(), "the second question is not relayed");
        assertTrue(state().concernSignals.get(ANALYST).contains("Unanswered question to k8s-workloads"));
        assertTrue(state().phaseRoster.get().signalled().contains(ANALYST), "the concern ends the asker's turn");
        var refused = events("question_asked").get(1);
        assertEquals("round_limit", refused.data().path("refusal").asText());
        agreesNow(THREAD, "a refused question is a concern in the log too");
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        assertTrue(state().phase != DiscussionOrchestrator.Phase.REVIEW, "the review settled on state");
    }

    @Test
    void aQuestionToAnAgentNotInTheCrewIsAConcern() throws Exception {
        toReview(WRONG);

        ask(ANALYST, "k8s-nodez");

        assertTrue(publishedOfType("question_asked").isEmpty());
        assertTrue(state().concernSignals.get(ANALYST).contains("k8s-nodez"));
        assertEquals("unknown_agent", events("question_asked").get(0).data().path("refusal").asText());
        agreesNow(THREAD, "an unknown target in the log");
    }

    @Test
    void zeroRoundsTurnTheRoundOff() throws Exception {
        maxRounds = 0;
        build(true);
        toReview(WRONG);

        ask(ANALYST, METRICS);

        assertTrue(publishedOfType("question_asked").isEmpty());
        assertTrue(state().concernSignals.containsKey(ANALYST));
        assertEquals("round_off", events("question_asked").get(0).data().path("refusal").asText());
    }

    @Test
    void aFailedRecheckLeavesTheQuestionAsTheAskersConcern() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        answer(METRICS, "failure", "model server down");

        assertTrue(publishedOfType("question_answered").isEmpty(), "nothing to wake the asker with");
        assertTrue(state().concernSignals.get(ANALYST).contains("could not recheck"));
        assertEquals(WRONG, state().agreeSignals.get(METRICS), "a failed recheck does not erase the finding");
        agreesNow(THREAD, "a failed recheck in the log");
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        assertTrue(state().phase != DiscussionOrchestrator.Phase.REVIEW, "the review settled on state");
    }

    @Test
    void anAnswerToAQuestionThatIsNotOpenChangesNothing() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        send(THREAD, WORKLOADS, "agree", "unrelated", "{\"answers\":\"" + relayedQuestionId() + "\"}");

        assertTrue(publishedOfType("question_answered").isEmpty(), "only the agent asked answers");
        assertTrue(state().recheck.inProgress());
    }

    @Test
    void aRestartedCoordinatorRebuildsTheRoundFromTheLog() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        var asked = ThreadRebuilder.build(CoordinatorStateReducer.reduce(forThread(THREAD)));
        assertTrue(asked.recheck.inProgress(), "the open question survives a restart");
        assertEquals(METRICS, asked.recheck.openFrom(ANALYST).target());
        assertEquals(QUESTION, asked.recheck.openFrom(ANALYST).text());

        answer(METRICS, "agree", REVISED);
        var answered = ThreadRebuilder.build(CoordinatorStateReducer.reduce(forThread(THREAD)));
        assertTrue(answered.recheck.openFrom(ANALYST).isAnswered(), "the answer is rebuilt");

        send(THREAD, ANALYST, "agree", "7 nodes checks out", null);
        var settled = ThreadRebuilder.build(CoordinatorStateReducer.reduce(forThread(THREAD)));
        assertFalse(settled.recheck.inProgress(), "the asker's signal settles the question in the log too");
        assertEquals(1, settled.recheck.history().size());
    }

    @Test
    void anAgentAskedThatMissesItsDeadlineLeavesTheQuestionAsAConcern() throws Exception {
        toReview(WRONG);
        when(triageModel.timeoutSeconds()).thenReturn(-1);
        ask(ANALYST, METRICS);

        checkWithoutSettling();

        assertTrue(state().concernSignals.get(ANALYST).contains("did not answer"));
        assertFalse(state().recheck.inProgress());
        assertEquals("failure", events("question_answered").get(0).data().path("signal").asText());
        assertTrue(ThreadRebuilder.build(CoordinatorStateReducer.reduce(forThread(THREAD))).concernSignals
                .containsKey(ANALYST), "a restarted coordinator rebuilds the concern");
    }

    @Test
    void withRoundsLeftTheAskerMayAskAgainAfterAnAnswer() throws Exception {
        maxRounds = 2;
        build(true);
        toReview(WRONG);
        ask(ANALYST, METRICS);
        answer(METRICS, "agree", REVISED);

        ask(ANALYST, WORKLOADS);

        assertEquals(2, publishedOfType("question_asked").size(), "the second question is relayed");
        assertTrue(state().pendingEvaluations.containsKey(WORKLOADS));
        assertFalse(state().concernSignals.containsKey(ANALYST));
    }

    @Test
    void aLateSignalFromTheAgentAskedKeepsTheQuestionOwed() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        send(THREAD, METRICS, "agree", "a late copy of the first finding", null);

        assertTrue(state().recheck.inProgress(), "the question is still owed an answer");
        assertTrue(state().pendingEvaluations.containsKey(METRICS), "the agent asked stays pending");
        assertTrue(publishedOfType("question_answered").isEmpty());
        checkWithoutSettling();
        assertEquals(DiscussionOrchestrator.Phase.REVIEW, state().phase);
    }

    @Test
    void aQuestionAfterTheReviewIsTheAskersConcernAndOpensNoRound() throws Exception {
        toReview(WRONG);
        send(THREAD, ANALYST, "agree", "checked", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        await(() -> state().phase == DiscussionOrchestrator.Phase.CLOSED, "the thread did not close: " + timeline);

        ask(ANALYST, METRICS);

        assertTrue(publishedOfType("question_asked").isEmpty(), "nothing is relayed after the review");
        assertFalse(state().recheck.inProgress(), "no round opens that nothing would close");
        assertTrue(state().concernSignals.get(ANALYST).contains("Unanswered question to k8s-metrics"));
    }

    @Test
    void aQuestionDuringSynthesisOrRatificationIsRefusedForThePhase() throws Exception {
        for (var phase : List.of(DiscussionOrchestrator.Phase.SYNTHESIZING, DiscussionOrchestrator.Phase.RATIFY,
                DiscussionOrchestrator.Phase.PAUSED)) {
            var thread = new DiscussionOrchestrator.ThreadState("t-" + phase);
            thread.phase = phase;
            var msg = MAPPER.readTree("{\"messageType\":\"question\",\"metadata\":{\"signalTo\":\"k8s-metrics\","
                    + "\"signalStatement\":\"recheck?\"}}");

            orchestrator.handleAgentSignal(thread, ANALYST, "question", "recheck?", msg);

            assertFalse(thread.recheck.inProgress(), phase + ": no round opens");
            assertTrue(thread.concernSignals.get(ANALYST).contains("not gathering or reviewing"), phase.name());
            assertFalse(thread.pendingEvaluations.containsKey(METRICS));
        }
        assertTrue(publishedOfType("question_asked").isEmpty(), "nothing is relayed");
    }

    @Test
    void anAskerThatCrashesWhileTheAgentRechecksLeavesNothingOpen() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);

        send(THREAD, ANALYST, "failure", "model server down", null);

        assertFalse(state().recheck.inProgress(), "the crashed asker's question is dropped");
        agreesNow(THREAD, "live and rebuilt state agree when the asker fails");
        assertFalse(ThreadRebuilder.build(CoordinatorStateReducer.reduce(forThread(THREAD))).recheck.inProgress());

        answer(METRICS, "concern", REVISED);

        assertTrue(publishedOfType("question_answered").isEmpty(), "nobody to wake");
        assertEquals(WRONG, state().agreeSignals.get(METRICS), "a late answer is a plain signal, not a revision");
    }

    @Test
    void anAnswerAfterTheQuestionTimedOutIsAPlainLateSignal() throws Exception {
        toReview(WRONG);
        when(triageModel.timeoutSeconds()).thenReturn(-1);
        ask(ANALYST, METRICS);
        checkWithoutSettling();
        String concern = state().concernSignals.get(ANALYST);

        answer(METRICS, "concern", REVISED);

        assertEquals(WRONG, state().agreeSignals.get(METRICS), "the earlier finding is not dropped");
        assertEquals(concern, state().concernSignals.get(ANALYST), "the timeout concern stays");
        assertTrue(publishedOfType("question_answered").isEmpty());
        assertTrue(forThread(THREAD).stream().noneMatch(e -> e.data().path("revises").asBoolean(false)),
                "the log records no revision");
    }

    @Test
    void aQuestionLoggedButNeverSentExpiresAfterARestart() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);
        // The coordinator stops between logging the question and sending it: a new one rebuilds the thread.
        var logged = CoordinatorStateReducer.reduce(forThread(THREAD));
        var rebuilt = ThreadRebuilder.build(logged);
        orchestrator.registerResumed(rebuilt);
        assertTrue(logged.pending.containsKey(METRICS), "the log keeps the agent asked pending");
        orchestrator.rearmDeadlines(rebuilt, logged.pending, logged.lastEventAt.toEpochMilli(),
                System.currentTimeMillis() - 36_000_000L);

        orchestrator.checkThreadTransition(rebuilt, Instant.now().plusSeconds(60));

        assertFalse(rebuilt.recheck.inProgress(), "the deadline ends the question");
        assertTrue(rebuilt.concernSignals.get(ANALYST).contains("did not answer"));
    }

    @Test
    void theAskersDeadlineAfterTheAnswerEndsTheRound() throws Exception {
        toReview(WRONG);
        ask(ANALYST, METRICS);
        when(triageModel.timeoutSeconds()).thenReturn(-1);
        answer(METRICS, "agree", REVISED);

        checkWithoutSettling();

        assertFalse(state().recheck.inProgress(), "an asker that missed its deadline settles nothing open");
        assertTrue(state().failureSignals.containsKey(ANALYST));
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        assertTrue(state().phase != DiscussionOrchestrator.Phase.REVIEW, "the review settled on state");
    }

    @Test
    void theAgentAskedMayAskBackInTheSameTurnWhenRoundsRemain() throws Exception {
        maxRounds = 2;
        build(true);
        toReview(WRONG);
        ask(ANALYST, METRICS);
        answer(METRICS, "agree", REVISED);
        send(THREAD, ANALYST, "agree", "7 nodes checks out", null);

        ask(METRICS, ANALYST);

        assertEquals(2, publishedOfType("question_asked").size());
        assertEquals(List.of(ANALYST), MAPPER.convertValue(
                publishedOfType("question_asked").get(1).body().path("metadata").path("innerCircle"), List.class));
        assertTrue(state().pendingEvaluations.containsKey(ANALYST));
    }

    @Test
    void theFastPathWaitsWhileAQuestionIsOpen() {
        var thread = new DiscussionOrchestrator.ThreadState("t-fast");
        thread.agreeSignals.put(METRICS, "a");
        thread.agreeSignals.put(WORKLOADS, "b");
        thread.lastSignalReceived = Instant.now().minusSeconds(60);
        assertTrue(orchestrator.hasSufficientConsensus(thread, Instant.now()));
        thread.recheck.ask("t-fast/0", ANALYST, METRICS, QUESTION, 1, java.util.Set.of(METRICS));
        assertFalse(orchestrator.hasSufficientConsensus(thread, Instant.now()),
                "two agrees do not settle the phase while a finding is being rechecked");
    }

    @Test
    void theRecheckRoundsAreKeptWithinWhatTheCrdAllows() {
        assertEquals(1, DiscussionOrchestrator.recheckRounds(Optional.empty()));
        assertEquals(0, DiscussionOrchestrator.recheckRounds(Optional.of(0)));
        assertEquals(5, DiscussionOrchestrator.recheckRounds(Optional.of(9)));
        assertEquals(0, DiscussionOrchestrator.recheckRounds(Optional.of(-2)));
    }

    /** A concern as the agent publishes it: the verdict the signal tool recorded, as the runtime sends it. */
    private void concern(String asker, String to, boolean startsRecheck) throws Exception {
        var call = new ai.kubemoot.agent.signal.SignalCall("concern", to, QUESTION, "", "");
        var verdict = SignalVerdicts.of(call, new SignalVerdicts.Finding("", ""), true, startsRecheck);
        var meta = MAPPER.createObjectNode().put("signal", verdict.signal());
        verdict.metadata().forEach((k, v) -> meta.put(k, String.valueOf(v)));
        send(THREAD, asker, verdict.signal(), verdict.content(), meta.toString());
    }

    @Test
    void aConcernThatNamesAnAgentStartsItsRecheckAndTheAskerSignalsAgain() throws Exception {
        toReview(WRONG);

        concern(ANALYST, METRICS, true);

        assertEquals(1, publishedOfType("question_asked").size(), "the doubted agent is asked to recheck");
        assertTrue(state().pendingEvaluations.containsKey(METRICS));
        assertFalse(state().phaseRoster.get().signalled().contains(ANALYST), "the asker waits for the answer");
        answer(METRICS, "agree", REVISED);
        assertEquals(REVISED, state().agreeSignals.get(METRICS));
        assertTrue(state().pendingEvaluations.containsKey(ANALYST), "the asker is woken to signal again");
        send(THREAD, ANALYST, "agree", "7 nodes checks out", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
    }

    @Test
    void aConcernNamingNoAgentIsANoteAndStartsNothing() throws Exception {
        toReview(WRONG);

        concern(ANALYST, "", true);

        assertTrue(publishedOfType("question_asked").isEmpty());
        assertTrue(state().concernSignals.containsKey(ANALYST));
        assertTrue(state().phaseRoster.get().signalled().contains(ANALYST), "the concern ends the turn");
    }

    @Test
    void aSecondAddressedConcernPastMaxRoundsIsANote() throws Exception {
        toReview(WRONG);
        concern(ANALYST, METRICS, true);
        answer(METRICS, "agree", REVISED);

        concern(ANALYST, WORKLOADS, true);

        assertEquals(1, publishedOfType("question_asked").size(), "maxRounds is 1");
        assertEquals("round_limit", events("question_asked").get(1).data().path("refusal").asText());
        assertTrue(state().concernSignals.containsKey(ANALYST));
    }
}
