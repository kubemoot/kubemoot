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
    void aMethodSaysWhetherTheDecisionMayBeCarriedOut() {
        var s = thread();
        recorder.opened(s, "request");

        assertTrue(recorder.phaseEntered(s, "thread_start"), "in the log");
        log.failEventsOfType("settled");
        assertFalse(recorder.settled(s, "quiet"), "not in the log: the effect must not happen");
        assertEquals(1, log.counted("kubemoot_coordinator_log_appends_total", "lost"));
        assertFalse(recorder.fenced(s), "a failed append is not a fence");
        log.stopFailing("settled");
        assertTrue(recorder.settled(s, "quiet"), "the next append goes through");
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
        assertEquals(1, log.counted("kubemoot_coordinator_log_appends_total", "fenced"));
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
    void anEventWhoseAcknowledgementWasLostButWasStoredDoesNotFenceTheNextAppend() {
        var s = thread();
        recorder.opened(s, "request");
        log.storeNextEventOfTypeButLoseItsAck("phase_entered");

        assertFalse(recorder.phaseEntered(s, "thread_start"), "the answer never came, so the effect waits");
        assertTrue(recorder.settled(s, "quiet"), "the next append finds the earlier event was stored and carries on");

        assertFalse(recorder.fenced(s));
        assertEquals(List.of("thread_opened", "phase_entered", "settled"), log.types());
        assertEquals(2, s.trace.kept().stream().filter(e -> !e.type().equals("settled")).count(),
                "the stored event is kept for the check at close");
        assertEquals(0, log.counted("kubemoot_coordinator_log_appends_total", "fenced"));
    }

    @Test
    void aRefusalAfterALostAcknowledgementIsStillAFenceWhenAnotherWriterWrote() {
        var s = thread();
        recorder.opened(s, "request");
        log.failEventsOfType("phase_entered");
        assertFalse(recorder.phaseEntered(s, "thread_start"));
        log.stopFailing("phase_entered");
        log.anotherWriterAppendsTo("kubemoot.coordinator.ns.crew.t1");

        assertFalse(recorder.settled(s, "quiet"));

        assertTrue(recorder.fenced(s), "the last message is not one of ours");
    }

    @Test
    void aMessageIdIsNotDerivedFromAnEventOfAnEarlierRound() {
        var s = thread();
        recorder.opened(s, "request");
        recorder.synthesisDone(s, "synthesized", "general", "answer");
        recorder.reopened(s);

        assertNull(recorder.messageId(s, "synthesis_done", "synthesis"), "round 1 has not written its answer yet");
    }
}
