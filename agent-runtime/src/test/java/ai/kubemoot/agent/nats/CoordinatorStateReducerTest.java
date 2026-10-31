package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The reducer from hand-written event sequences, one per phase a thread can be in when a
 * coordinator stops. The sequences use the field names the recorder writes; the round trip
 * test checks the two against each other with the real orchestrator.
 */
class CoordinatorStateReducerTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String T = "t1";
    private static final String START = "2026-10-31T12:00:00Z";
    private static final String LATER = "2026-10-31T12:00:09Z";

    private static CoordinatorEvent ev(String id, String type, int round, String dataJson) {
        try {
            return new CoordinatorEvent(T + "/" + id, type, T, round, Instant.parse(START), "coordinator",
                    (ObjectNode) MAPPER.readTree(dataJson));
        } catch (Exception e) {
            throw new IllegalArgumentException(e);
        }
    }

    private static CoordinatorEvent opened() {
        return ev("opened", "thread_opened", 0, """
                {"crew":"crew","conversationId":"c1","userQuery":"how many pods","primaryChannel":"general",
                 "source":"request","openedAt":"%s","phaseStartedAt":"%s"}""".formatted(START, START));
    }

    private static CoordinatorEvent phase(String id, String phase, String reason) {
        return ev(id, "phase_entered", 0, """
                {"phase":"%s","phaseStartedAt":"%s","phaseBeforePause":null,"advisoryPending":%s,"reason":"%s"}"""
                .formatted(phase, LATER, "EVALUATING".equals(phase), reason));
    }

    private static CoordinatorEvent convened() {
        return ev("subcommittee", "subcommittee_convened", 0, """
                {"path":"reasoning","members":["metrics","workloads","analyst"],"brief":"Check pods.",
                 "technologies":["kubernetes"],"skills":["skill-a"],"confidence":0.9,"primaryChannel":"kubernetes",
                 "tokens":{"advisoryIn":100,"advisoryOut":20,"triageIn":0,"triageOut":0}}""");
    }

    private static CoordinatorEvent evaluationStarted() {
        return ev("evaluation/1", "evaluation_started", 0,
                "{\"roster\":[\"metrics\",\"workloads\"],\"startedAt\":\"" + LATER + "\"}");
    }

    private static CoordinatorEvent signal(String agent, String signal, String extra) {
        return ev("signal/" + agent + "/" + signal, "signal_applied", 0,
                "{\"agent\":\"" + agent + "\",\"signal\":\"" + signal + "\",\"messageId\":\"m-" + agent + "\""
                        + (extra.isEmpty() ? "" : "," + extra) + "}");
    }

    private static CoordinatorThreadState reduce(CoordinatorEvent... events) {
        return CoordinatorStateReducer.reduce(List.of(events));
    }

    // --- one sequence per phase ---

    @Test
    void anEmptyLogGivesAnEmptyThread() {
        var s = reduce();

        assertNull(s.threadId);
        assertEquals("SUBMITTED", s.phase);
    }

    @Test
    void afterThreadOpened_theThreadIsKnownButNotStarted() {
        var s = reduce(opened());

        assertEquals(T, s.threadId);
        assertEquals("how many pods", s.userQuery);
        assertEquals("c1", s.conversationId);
        assertEquals("request", s.source);
        assertEquals("SUBMITTED", s.phase);
        assertEquals(Instant.parse(START), s.openedAt);
    }

    @Test
    void inAdvisory_thePhaseAndItsClockAreSet() {
        var s = reduce(opened(), phase("phase/ADVISORY/1", "ADVISORY", "thread_start"));

        assertEquals("ADVISORY", s.phase);
        assertEquals(Instant.parse(LATER), s.phaseStartedAt);
        assertFalse(s.advisoryPending);
    }

    @Test
    void inEvaluating_theSubcommitteeAndTheRosterAreKnownAndAgentsArePending() {
        var s = reduce(opened(), phase("phase/ADVISORY/1", "ADVISORY", "thread_start"),
                phase("phase/EVALUATING/1", "EVALUATING", "advisory"), convened(), evaluationStarted(),
                signal("metrics", "triaging", "\"deadlineAt\":1000"),
                signal("workloads", "evaluating", "\"deadlineAt\":2000"));

        assertEquals("EVALUATING", s.phase);
        assertFalse(s.advisoryPending, "evaluation_started ends the advisory wait");
        assertEquals(Set.of("analyst", "metrics", "workloads"), s.innerCircle);
        assertEquals(Set.of("skill-a"), s.selectedSkills);
        assertEquals(0.9, s.triageConfidence);
        assertEquals("kubernetes", s.primaryChannel);
        assertEquals(100, s.advisoryIn);
        assertEquals(Set.of("metrics", "workloads"), s.rosterMembers);
        assertEquals(Map.of("metrics", 1000L, "workloads", 2000L), s.pending);
    }

    @Test
    void whileTheAdvisoryRuns_theThreadIsEvaluatingWithTheAdvisoryPending() {
        var s = reduce(opened(), phase("phase/EVALUATING/1", "EVALUATING", "advisory"));

        assertTrue(s.advisoryPending);
        assertTrue(s.rosterMembers.isEmpty(), "the roster is unknown until the evaluation starts");
    }

    @Test
    void aTerminalSignalClearsTheAgentFromPendingAndMarksItSignalledOnTheRoster() {
        var s = reduce(opened(), evaluationStarted(),
                signal("metrics", "evaluating", "\"deadlineAt\":1000"),
                signal("metrics", "agree", "\"researcher\":true,\"lastSignalAt\":\"" + LATER + "\""),
                signal("outsider", "agree", "\"lastSignalAt\":\"" + LATER + "\""));

        assertTrue(s.pending.isEmpty());
        assertEquals(Set.of("metrics"), s.rosterSignalled, "an agent off the roster does not count for it");
        assertEquals(Set.of("metrics", "outsider"), s.agree.keySet());
        assertEquals(Set.of("metrics"), s.researchers);
        assertEquals(Instant.parse(LATER), s.lastSignalAt);
    }

    @Test
    void everySignalKindLandsInItsOwnBucket() {
        var s = reduce(opened(), evaluationStarted(),
                signal("a", "agree", ""), signal("b", "concern", ""), signal("c", "block", ""),
                signal("d", "stand_aside", ""),
                signal("e", "stand_aside", "\"capacityReason\":\"model-too-large\",\"tooLargeModel\":\"big:70b\""),
                signal("f", "stand_aside", "\"capacityReason\":\"gpu-busy\""),
                signal("g", "waiting", "\"model\":\"m:14b\""));

        assertEquals(Set.of("a"), s.agree.keySet());
        assertEquals(Set.of("b"), s.concern.keySet());
        assertEquals(Set.of("c"), s.block.keySet());
        assertEquals(Set.of("d", "e", "f"), s.standAside);
        assertEquals(Map.of("e", "model-too-large", "f", "gpu-busy"), s.capacityStandAsides);
        assertEquals(Set.of("big:70b"), s.tooLargeModels);
        assertEquals(Map.of("g", "m:14b"), s.waiting);
    }

    @Test
    void anAgentThatStartsEvaluatingStopsWaiting() {
        var s = reduce(opened(), evaluationStarted(), signal("g", "waiting", "\"model\":\"m\",\"deadlineAt\":5"),
                signal("g", "evaluating", "\"deadlineAt\":9"));

        assertTrue(s.waiting.isEmpty());
        assertEquals(Map.of("g", 9L), s.pending);
    }

    @Test
    void aReportedFailureAndASynthesizedOneAreBothRecordedAsFailures() {
        var reported = ev("failure/a/m1", "failure_recorded", 0, """
                {"agent":"a","cause":"reported","synthesized":false,"failedSources":["pods_top"],
                 "messageId":"m1","lastSignalAt":"%s"}""".formatted(LATER));
        var timedOut = ev("failure/b/timeout", "failure_recorded", 0, """
                {"agent":"b","cause":"evaluation_timeout","synthesized":true,"failedSources":[],
                 "messageId":"","lastSignalAt":"%s"}""".formatted(LATER));

        var s = reduce(opened(), evaluationStarted(), signal("b", "waiting", "\"model\":\"m\",\"deadlineAt\":5"),
                reported, timedOut);

        assertEquals(Set.of("a", "b"), s.failure.keySet());
        assertEquals("evaluation_timeout", s.failure.get("b"));
        assertEquals(Set.of("pods_top"), s.failedSources);
        assertEquals(Map.of("b", "m"), s.waiting, "a synthesized failure leaves the agent's wait as it was");
        assertTrue(s.pending.isEmpty());
    }

    @Test
    void inReview_theRosterIsTheAnalystsAndTheyArePending() {
        var s = reduce(opened(), evaluationStarted(), signal("metrics", "agree", ""),
                ev("settled/EVALUATING/1", "settled", 0,
                        "{\"phase\":\"EVALUATING\",\"basis\":\"roster_signalled\",\"counts\":{}}"),
                ev("review/1", "review_started", 0,
                        "{\"roster\":[\"analyst\"],\"pending\":{\"analyst\":7000}}"),
                phase("phase/REVIEW/1", "REVIEW", "settled"));

        assertEquals("REVIEW", s.phase);
        assertEquals(Set.of("analyst"), s.rosterMembers);
        assertTrue(s.rosterSignalled.isEmpty());
        assertEquals(Map.of("analyst", 7000L), s.pending);
        assertEquals(Set.of("metrics"), s.agree.keySet(), "review keeps the evaluation's signals");
        assertEquals("EVALUATING", s.lastSettledPhase);
        assertEquals("roster_signalled", s.lastSettleBasis);
    }

    @Test
    void inSynthesis_theAnswerIsKnownOnceItIsWrittenAheadOfPublishing() {
        var s = reduce(opened(), phase("phase/SYNTHESIZING/1", "SYNTHESIZING", "settled"),
                ev("synthesis/started/1", "synthesis_started", 0, "{\"agrees\":2}"),
                ev("synthesis/done/1", "synthesis_done", 0, """
                        {"outcome":"synthesized","channel":"general","response":"Eleven pods.",
                         "tokens":{"in":300,"out":40},"promptTooLarge":false}"""));

        assertEquals("SYNTHESIZING", s.phase, "the thread is not closed until thread_closed");
        assertTrue(s.synthesisStarted);
        assertEquals("synthesized", s.synthesisOutcome);
        assertEquals("Eleven pods.", s.answer);
        assertEquals(300, s.synthesisIn);
        assertEquals(40, s.synthesisOut);
    }

    @Test
    void whenClosed_theReasonIsKept() {
        var s = reduce(opened(), phase("phase/SYNTHESIZING/1", "SYNTHESIZING", "settled"),
                ev("closed", "thread_closed", 0, "{\"reason\":\"synthesized\",\"channel\":\"general\"}"));

        assertTrue(s.closed());
        assertEquals("synthesized", s.closeReason);
    }

    @Test
    void whenPaused_thePhaseBeforeThePauseIsSaved() {
        var paused = ev("phase/PAUSED/1", "phase_entered", 0, """
                {"phase":"PAUSED","phaseStartedAt":"%s","phaseBeforePause":"REVIEW","advisoryPending":false,
                 "reason":"pause"}""".formatted(LATER));
        var resumed = ev("phase/REVIEW/2", "phase_entered", 0, """
                {"phase":"REVIEW","phaseStartedAt":"%s","phaseBeforePause":null,"advisoryPending":false,
                 "reason":"resume"}""".formatted(START));

        var whilePaused = reduce(opened(), phase("phase/REVIEW/1", "REVIEW", "settled"), paused);
        var after = reduce(opened(), phase("phase/REVIEW/1", "REVIEW", "settled"), paused, resumed);

        assertEquals("PAUSED", whilePaused.phase);
        assertEquals("REVIEW", whilePaused.phaseBeforePause);
        assertEquals("REVIEW", after.phase);
        assertNull(after.phaseBeforePause);
        assertEquals(Instant.parse(START), after.phaseStartedAt, "a resume restarts the phase clock");
    }

    @Test
    void aReopenedThreadStartsANewRoundWithNoSignalsAndNoRoster() {
        var reopened = ev("reopened/1", "thread_reopened", 1,
                "{\"phaseStartedAt\":\"" + LATER + "\",\"roster\":[]}");

        var s = reduce(opened(), evaluationStarted(), signal("metrics", "agree", "\"lastSignalAt\":\"" + LATER + "\""),
                ev("closed", "thread_closed", 0, "{\"reason\":\"synthesized\"}"), reopened);

        assertEquals("EVALUATING", s.phase);
        assertEquals(1, s.round);
        assertTrue(s.agree.isEmpty());
        assertTrue(s.rosterMembers.isEmpty());
        assertNull(s.closeReason);
        assertEquals(Instant.parse(LATER), s.lastSignalAt, "the quiet-window time is kept, as the orchestrator keeps it");
    }

    // --- idempotency and tolerance ---

    @Test
    void aDuplicateEventIsIgnored() {
        var agree = signal("metrics", "agree", "");
        var state = new CoordinatorThreadState();

        assertTrue(CoordinatorStateReducer.apply(state, opened()));
        assertTrue(CoordinatorStateReducer.apply(state, agree));
        var once = state.view();
        assertFalse(CoordinatorStateReducer.apply(state, agree), "the second copy is not applied");

        assertEquals(once, state.view());
    }

    @Test
    void replayingTheWholeSequenceAgainChangesNothing() {
        var events = List.of(opened(), phase("phase/EVALUATING/1", "EVALUATING", "advisory"), convened(),
                evaluationStarted(), signal("metrics", "agree", ""));
        var twice = new ArrayList<>(events);
        twice.addAll(events);

        assertEquals(CoordinatorStateReducer.reduce(events).view(), CoordinatorStateReducer.reduce(twice).view());
    }

    @Test
    void aDuplicateThatArrivesAfterLaterEventsDoesNotUndoThem() {
        var first = signal("metrics", "evaluating", "\"deadlineAt\":1000");
        var agreed = signal("metrics", "agree", "");

        var s = reduce(opened(), evaluationStarted(), first, agreed, first);

        assertTrue(s.pending.isEmpty(), "the repeated evaluating signal must not put the agent back on pending");
    }

    @Test
    void anUnknownEventTypeIsIgnored() {
        var future = ev("something", "quorum_changed", 0, "{\"anything\":1}");
        var state = new CoordinatorThreadState();
        CoordinatorStateReducer.apply(state, opened());
        var before = state.view();

        assertFalse(CoordinatorStateReducer.apply(state, future));

        assertEquals(before, state.view());
        assertFalse(state.appliedEventIds.contains(future.eventId()), "an ignored event is not remembered as applied");
    }

    @Test
    void anUnknownSignalNameIsIgnored() {
        var s = reduce(opened(), evaluationStarted(), signal("metrics", "telepathy", ""));

        assertTrue(s.agree.isEmpty() && s.pending.isEmpty());
    }

    @Test
    void anEventOfAnotherThreadIsIgnored() {
        var other = new CoordinatorEvent("t2/signal", "signal_applied", "t2", 0, Instant.parse(START), "c",
                (ObjectNode) MAPPER.createObjectNode().put("agent", "a").put("signal", "agree"));
        var state = new CoordinatorThreadState();
        CoordinatorStateReducer.apply(state, opened());

        assertFalse(CoordinatorStateReducer.apply(state, other));
        assertTrue(state.agree.isEmpty());
    }

    @Test
    void aNullEventIsIgnored() {
        assertFalse(CoordinatorStateReducer.apply(new CoordinatorThreadState(), null));
    }
}
