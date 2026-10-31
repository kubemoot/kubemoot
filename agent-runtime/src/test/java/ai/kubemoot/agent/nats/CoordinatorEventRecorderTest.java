package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** What the recorder writes, and when it writes nothing. */
class CoordinatorEventRecorderTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final RecordingEventLog log = new RecordingEventLog();
    private final CoordinatorEventRecorder recorder = log.recorder();

    private static ThreadState thread() {
        var s = new ThreadState("t1");
        s.crew = "crew";
        s.userQuery = "how many pods";
        s.primaryChannel = "general";
        return s;
    }

    private static com.fasterxml.jackson.databind.JsonNode msg(String json) {
        try {
            return MAPPER.readTree(json);
        } catch (Exception e) {
            throw new IllegalArgumentException(e);
        }
    }

    @Test
    void anOffRecorderWritesNothingAndSkipsTheWork() {
        var off = CoordinatorEventRecorder.off();
        var s = thread();

        off.opened(s, "request");
        off.phaseEntered(s, "thread_start");

        assertFalse(off.enabled());
        assertFalse(s.trace.opened(), "an off recorder does not even mark the thread");
    }

    @Test
    void aThreadThatWasNeverOpenedWritesNoEvents() {
        var s = thread();

        recorder.phaseEntered(s, "thread_start");
        recorder.signalApplied(s, "metrics", "agree", msg("{\"messageId\":\"m1\"}"));
        recorder.closed(s, "zombie", "general");

        assertTrue(log.events().isEmpty(), "a late message for an evicted thread must not write to the log");
    }

    @Test
    void openingAThreadWritesItsQuestionAndStartTime() {
        var s = thread();

        recorder.opened(s, "request");

        var event = log.events().get(0);
        assertEquals("thread_opened", event.type());
        assertEquals("t1/opened", event.eventId());
        assertEquals("how many pods", event.data().path("userQuery").asText());
        assertEquals("request", event.data().path("source").asText());
        assertEquals(s.threadCreated.toString(), event.data().path("openedAt").asText());
        assertEquals("coordinator", event.coordinator());
        assertEquals("kubemoot.coordinator.ns.crew.t1", log.subjects().get(0));
    }

    @Test
    void enteringAPhaseAgainAfterAPauseGetsANewId() {
        var s = thread();
        recorder.opened(s, "request");
        s.phase = Phase.REVIEW;

        recorder.phaseEntered(s, "settled");
        recorder.phaseEntered(s, "resume");

        var ids = log.events().stream().map(CoordinatorEvent::eventId).toList();
        assertEquals("t1/0/phase/REVIEW/1", ids.get(1));
        assertEquals("t1/0/phase/REVIEW/2", ids.get(2), "re-entering a phase (pause then resume) is a new event");
    }

    @Test
    void aSignalEventIsKeyedByTheMessageThatCarriedIt_soARedeliveredMessageHasTheSameId() {
        var s = thread();
        recorder.opened(s, "request");
        var message = msg("{\"messageId\":\"m-7\"}");

        assertTrue(recorder.signalApplied(s, "metrics", "agree", message));
        assertTrue(recorder.signalApplied(s, "metrics", "agree", message), "a repeat is answered as already written");

        var ids = log.events().stream().skip(1).map(CoordinatorEvent::eventId).toList();
        assertEquals(List.of("t1/0/signal/metrics/m-7"), ids, "the stream drops the second by this id");
    }

    @Test
    void aHeartbeatAndAnUnknownMessageTypeWriteNothing() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.signalApplied(s, "metrics", "heartbeat", msg("{\"messageId\":\"m1\"}"));
        recorder.signalApplied(s, "metrics", "advisory", msg("{\"messageId\":\"m2\"}"));

        assertEquals(1, log.events().size());
    }

    @Test
    void aSignalWithNoMessageIsRecordedWithoutFailing() {
        var s = thread();
        recorder.opened(s, "request");

        assertDoesNotThrow(() -> recorder.signalApplied(s, "metrics", "agree", null));
        assertDoesNotThrow(() -> recorder.signalApplied(s, "metrics", "failure", null));

        assertEquals(3, log.events().size());
    }

    @Test
    void theAliasesFoldIntoTheirSignalNames() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.signalApplied(s, "a", "contribution", msg("{\"messageId\":\"1\"}"));
        recorder.signalApplied(s, "b", "decline", msg("{\"messageId\":\"2\"}"));

        assertEquals("agree", log.events().get(1).data().path("signal").asText());
        assertEquals("stand_aside", log.events().get(2).data().path("signal").asText());
    }

    @Test
    void aStandAsideForWantOfAnAcceleratorNamesTheReasonAndTheTooLargeModel() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.signalApplied(s, "a", "stand_aside",
                msg("{\"messageId\":\"1\",\"metadata\":{\"reason\":\"model-too-large\",\"model\":\"big:70b\"}}"));
        recorder.signalApplied(s, "b", "stand_aside",
                msg("{\"messageId\":\"2\",\"metadata\":{\"reason\":\"nothing-to-add\"}}"));

        var tooLarge = log.events().get(1).data();
        assertEquals("model-too-large", tooLarge.path("capacityReason").asText());
        assertEquals("big:70b", tooLarge.path("tooLargeModel").asText());
        assertTrue(log.events().get(2).data().path("capacityReason").isMissingNode(),
                "an ordinary stand-aside carries no capacity reason");
    }

    @Test
    void aClosedThreadSaysHowItEnded() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.synthesisDone(s, "fallback", "general", "Here is what the toolers found");
        recorder.closed(s, "fallback", "general");

        var done = log.events().get(1);
        assertEquals("synthesis_done", done.type());
        assertEquals("Here is what the toolers found", done.data().path("response").asText());
        assertEquals("fallback", log.events().get(2).data().path("reason").asText());
        assertEquals("t1/0/closed", log.events().get(2).eventId());
    }

    @Test
    void reopeningStartsANewRoundThatKeysLaterEvents() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.reopened(s);
        recorder.settled(s, "quiet");

        assertEquals("t1/reopened/1", log.events().get(1).eventId());
        assertEquals(1, log.events().get(1).round());
        assertTrue(log.events().get(2).eventId().startsWith("t1/1/settled/"), log.events().get(2).eventId());
    }

    @Test
    void anEventThatCannotBeBuiltIsDroppedAndNeverReachesTheOrchestrator() {
        var s = thread();
        recorder.opened(s, "request");

        assertDoesNotThrow(() -> recorder.subcommitteeConvened(s, null, null));

        assertEquals(1, log.events().size(), "only thread_opened was written");
    }

    @Test
    void twoSignalsFromOneAgentWithNoMessageIdOrTimestampStillGetDifferentIds() {
        var s = thread();
        recorder.opened(s, "request");

        recorder.signalApplied(s, "metrics", "agree", msg("{}"));
        recorder.signalApplied(s, "metrics", "agree", msg("{}"));

        assertFalse(log.events().get(1).eventId().equals(log.events().get(2).eventId()));
    }

    @Test
    void fromConfig_isOffUnlessTheSettingIsTrue() {
        var provider = org.mockito.Mockito.mock(NatsConnectionProvider.class);
        try {
            System.clearProperty(CoordinatorEventRecorder.CONFIG_KEY);
            assertFalse(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER, null).enabled(), "unset is off");

            System.setProperty(CoordinatorEventRecorder.CONFIG_KEY, "false");
            assertFalse(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER, null).enabled());

            System.setProperty(CoordinatorEventRecorder.CONFIG_KEY, "true");
            assertTrue(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER, null).enabled());
        } finally {
            System.clearProperty(CoordinatorEventRecorder.CONFIG_KEY);
        }
    }

    @Test
    void anOrchestratorBuiltWithoutTheSettingWritesNothing() {
        var s = thread();
        var off = CoordinatorEventRecorder.off();

        off.signalApplied(s, "metrics", "agree", msg("{\"messageId\":\"m\"}"));
        off.timedOut(s, "metrics");
        off.closed(s, "zombie", "general");

        assertFalse(s.trace.opened());
        assertEquals(0, s.trace.enter("none") - 1, "an off recorder leaves the counters alone");
    }

    @Test
    void aLogThatThrowsNeverReachesTheOrchestrator() {
        CoordinatorEventLog broken = (subject, event, expected) -> {
            throw new IllegalStateException("stream gone");
        };
        var failing = new CoordinatorEventRecorder(true, broken, (crew, id) -> id, "c", MAPPER, Instant::now, log.metrics());
        var s = thread();

        assertDoesNotThrow(() -> failing.opened(s, "request"));
    }

    @Test
    void aSubjectBuilderThatThrowsNeverReachesTheOrchestrator() {
        var failing = new CoordinatorEventRecorder(true, log, (crew, id) -> {
            throw new IllegalStateException("no scope");
        }, "c", MAPPER, Instant::now, log.metrics());

        assertDoesNotThrow(() -> failing.opened(thread(), "request"));
        assertTrue(log.events().isEmpty());
    }

    // --- write-ahead, fencing, derived ids, the check at close ---

    @Test
    void aFailedAppendLetsTheDecisionProceedMarksTheThreadUnloggedAndStopsAppending() {
        var s = thread();
        recorder.opened(s, "request");
        assertTrue(recorder.phaseEntered(s, "thread_start"), "in the log");
        log.failEventsOfType("settled");

        assertTrue(recorder.settled(s, "quiet"), "the log is down: the effect is published anyway");

        assertTrue(recorder.unlogged(s));
        assertFalse(recorder.fenced(s), "a failed append is not a fence");
        assertEquals(1, log.counted("kubemoot_coordinator_log_append_failures_total"));
        assertEquals(1, log.counted("kubemoot_coordinator_threads_unlogged_total"));
        log.stopFailing("settled");
        int stored = log.events().size();
        assertTrue(recorder.synthesisDone(s, "synthesized", "general", "answer"), "decisions are still allowed");
        recorder.closed(s, "synthesized", "general");
        assertEquals(stored, log.events().size(), "its log is incomplete, so nothing more is appended");
        assertEquals(1, log.counted("kubemoot_coordinator_threads_unlogged_total"), "reported once per thread");
    }

    @Test
    void anUnloggedThreadGetsRandomMessageIdsAndIsCountedUnloggedAtClose() {
        var s = thread();
        recorder.opened(s, "request");
        recorder.synthesisDone(s, "synthesized", "general", "answer");
        log.failEventsOfType("settled");
        recorder.settled(s, "quiet");

        assertNull(recorder.messageId(s, "synthesis_done", "synthesis"));
        assertNull(recorder.closeMessageId(s));
        recorder.checkAtClose(s);

        assertEquals(1, log.counted("kubemoot_coordinator_log_checks_total", "unlogged"));
        assertEquals(0, log.counted("kubemoot_coordinator_log_checks_total", "match"));
        assertEquals(0, log.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
    }

    @Test
    void anOffRecorderAlwaysAllowsTheDecision() {
        var off = CoordinatorEventRecorder.off();

        assertTrue(off.synthesisDone(thread(), "synthesized", "general", "answer"));
        assertFalse(off.fenced(thread()));
    }

    @Test
    void aWriterAnotherOneOutranIsFencedAndNothingMoreIsWrittenForTheThread() {
        var s = thread();
        recorder.opened(s, "request");
        log.anotherWriterAppendsTo("kubemoot.coordinator.ns.crew.t1");

        assertFalse(recorder.phaseEntered(s, "thread_start"), "refused: another writer got there first");

        assertTrue(recorder.fenced(s));
        int stored = log.events().size();
        assertFalse(recorder.settled(s, "quiet"), "a fenced thread permits nothing");
        assertFalse(recorder.synthesisDone(s, "synthesized", "general", "answer"));
        assertEquals(stored, log.events().size());
        assertEquals(1, log.counted("kubemoot_coordinator_log_fenced_total"));
    }

    @Test
    void eachAppendStatesTheSequenceOfTheOneBefore() {
        var s = thread();
        recorder.opened(s, "request");
        recorder.phaseEntered(s, "thread_start");
        recorder.settled(s, "quiet");

        assertEquals(List.of(0L, 1L, 2L), log.expectedSequences());
        assertEquals(3, s.trace.lastSequence());
    }

    @Test
    void aMessageIdIsDerivedFromTheEventThatDecidedIt() {
        var s = thread();
        recorder.opened(s, "request");
        assertNull(recorder.messageId(s, "synthesis_done", "synthesis"), "no such event yet");

        recorder.synthesisDone(s, "synthesized", "general", "answer");

        assertEquals("t1/0/synthesis/done/1/msg/synthesis", recorder.messageId(s, "synthesis_done", "synthesis"));
        assertEquals("t1/0/closed/msg/thread_close", recorder.closeMessageId(s));
        assertNull(CoordinatorEventRecorder.off().messageId(s, "synthesis_done", "synthesis"));
        assertNull(CoordinatorEventRecorder.off().closeMessageId(s));
    }

    @Test
    void aRepeatedDecisionHasTheSameMessageId() {
        var first = thread();
        var second = thread();
        var other = new RecordingEventLog().recorder();
        recorder.opened(first, "request");
        other.opened(second, "request");

        recorder.synthesisDone(first, "synthesized", "general", "answer");
        other.synthesisDone(second, "synthesized", "general", "answer");

        assertEquals(recorder.messageId(first, "synthesis_done", "synthesis"),
                other.messageId(second, "synthesis_done", "synthesis"));
    }

    @Test
    void theCheckAtCloseCountsAMatchWhenTheLogRebuildsTheThread() {
        var s = thread();
        recorder.opened(s, "request");
        s.phase = Phase.CLOSED;
        recorder.closed(s, "zombie", "general");

        recorder.checkAtClose(s);

        assertEquals(1, log.counted("kubemoot_coordinator_log_checks_total", "match"));
        assertEquals(0, log.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
    }

    @Test
    void theCheckAtCloseCountsAMismatchWhenTheLiveStateMovedWithoutAnEvent() {
        var s = thread();
        recorder.opened(s, "request");
        s.phase = Phase.CLOSED;
        recorder.closed(s, "zombie", "general");
        s.agreeSignals.put("metrics", "11 pods");

        assertDoesNotThrow(() -> recorder.checkAtClose(s));

        assertEquals(1, log.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
    }

    @Test
    void theCheckAtCloseSkipsAThreadItDoesNotOwnOrNeverLogged() {
        var fenced = thread();
        recorder.opened(fenced, "request");
        fenced.trace.fence();
        recorder.checkAtClose(fenced);
        recorder.checkAtClose(thread());
        CoordinatorEventRecorder.off().checkAtClose(thread());

        assertEquals(0, log.counted("kubemoot_coordinator_log_checks_total", "match"));
        assertEquals(0, log.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
    }

    @Test
    void aMessageIdIsNotDerivedFromAnEventOfAnEarlierRound() {
        var s = thread();
        recorder.opened(s, "request");
        recorder.synthesisDone(s, "synthesized", "general", "answer");
        recorder.reopened(s);

        assertNull(recorder.messageId(s, "synthesis_done", "synthesis"), "round 1 has not written its answer yet");
    }

    @Test
    void anEventThatCannotBeBuiltMakesTheThreadUnloggedAndLetsTheDecisionProceed() {
        var failing = new CoordinatorEventRecorder(true, log, (crew, id) -> {
            throw new IllegalStateException("no scope");
        }, "c", MAPPER, Instant::now, log.metrics());
        var s = thread();
        failing.opened(s, "request");

        assertTrue(failing.phaseEntered(s, "thread_start"), "the discussion goes on");

        assertTrue(failing.unlogged(s));
        assertEquals(1, log.counted("kubemoot_coordinator_threads_unlogged_total"));
        assertTrue(log.events().isEmpty());
    }

    @Test
    void aWriterQueuedBehindAFailedAppendAppendsNothing() throws Exception {
        var inFirstAppend = new java.util.concurrent.CountDownLatch(1);
        var releaseFirst = new java.util.concurrent.CountDownLatch(1);
        var appends = new java.util.concurrent.atomic.AtomicInteger();
        CoordinatorEventLog slowThenDown = (subject, event, expected) -> {
            if (appends.incrementAndGet() == 1) {
                inFirstAppend.countDown();
                try {
                    releaseFirst.await();
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                }
            }
            return CoordinatorEventLog.AppendResult.failed();
        };
        var slow = new CoordinatorEventRecorder(true, slowThenDown, (crew, id) -> id, "c", MAPPER, Instant::now,
                log.metrics());
        var s = thread();
        s.trace.markOpened();

        var first = java.util.concurrent.CompletableFuture.runAsync(() -> slow.phaseEntered(s, "thread_start"));
        inFirstAppend.await();
        var second = java.util.concurrent.CompletableFuture.runAsync(() -> slow.settled(s, "quiet"));
        Thread.sleep(100);
        releaseFirst.countDown();
        first.get(5, java.util.concurrent.TimeUnit.SECONDS);
        second.get(5, java.util.concurrent.TimeUnit.SECONDS);

        assertEquals(1, appends.get(), "the second writer found the thread unlogged and appended nothing");
        assertTrue(slow.unlogged(s));
    }
}
