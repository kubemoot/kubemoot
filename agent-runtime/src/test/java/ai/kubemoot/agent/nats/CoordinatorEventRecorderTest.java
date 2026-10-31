package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
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

        recorder.signalApplied(s, "metrics", "agree", message);
        recorder.signalApplied(s, "metrics", "agree", message);

        var ids = log.events().stream().skip(1).map(CoordinatorEvent::eventId).toList();
        assertEquals("t1/0/signal/metrics/m-7", ids.get(0));
        assertEquals(ids.get(0), ids.get(1), "the stream and the reducer drop the second by this id");
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
            assertFalse(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER).enabled(), "unset is off");

            System.setProperty(CoordinatorEventRecorder.CONFIG_KEY, "false");
            assertFalse(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER).enabled());

            System.setProperty(CoordinatorEventRecorder.CONFIG_KEY, "true");
            assertTrue(CoordinatorEventRecorder.fromConfig("c", provider, MAPPER).enabled());
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
        CoordinatorEventLog broken = (subject, event) -> {
            throw new IllegalStateException("stream gone");
        };
        var failing = new CoordinatorEventRecorder(true, broken, (crew, id) -> id, "c", MAPPER, Instant::now);
        var s = thread();

        assertDoesNotThrow(() -> failing.opened(s, "request"));
    }

    @Test
    void aSubjectBuilderThatThrowsNeverReachesTheOrchestrator() {
        var failing = new CoordinatorEventRecorder(true, log, (crew, id) -> {
            throw new IllegalStateException("no scope");
        }, "c", MAPPER, Instant::now);

        assertDoesNotThrow(() -> failing.opened(thread(), "request"));
        assertTrue(log.events().isEmpty());
    }
}
