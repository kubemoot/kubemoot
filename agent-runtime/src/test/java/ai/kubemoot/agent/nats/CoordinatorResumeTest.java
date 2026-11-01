package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A coordinator that stops and restarts picks its threads up where the log left them. Each
 * test runs a scripted thread to its end, then restarts the coordinator after every event it
 * wrote, with the log and the bus as the first coordinator left them, and checks the thread
 * ends where the uninterrupted run did.
 */
class CoordinatorResumeTest extends ResumeRestartHarness {

    // --- a restart after every event ---

    @Test
    void aThreadThatReviewsEndsTheSameAfterARestartAtAnyEvent() throws Exception {
        var actions = restartAtEveryEvent(fullScript(), true, SELECT_THREE);

        assertTrue(actions.containsAll(Set.of(
                ResumeContinuation.SWEEP, ResumeContinuation.RERUN_ADVISORY, ResumeContinuation.ANNOUNCE_SUBCOMMITTEE,
                ResumeContinuation.REPUBLISH_ADVISORY_READY, ResumeContinuation.ENTER_REVIEW,
                ResumeContinuation.REPUBLISH_REVIEW_READY, ResumeContinuation.RESTART_SYNTHESIS,
                ResumeContinuation.FINISH_CLOSE)), "every row of the resume table was taken at some event: " + actions);
    }

    @Test
    void aThreadThatSkipsReviewEndsTheSameAfterARestartAtAnyEvent() throws Exception {
        selectionIs(SELECT_TWO);
        restartAtEveryEvent(skipReviewScript(), false, SELECT_TWO);
    }

    @Test
    void aThreadTheCoordinatorAnswersItselfEndsTheSameAfterARestartAtAnyEvent() throws Exception {
        var actions = restartAtEveryEvent(directScript(), true, SELECT_NONE);

        assertTrue(actions.contains(ResumeContinuation.ANSWER_DIRECTLY), "the direct answer was written again: " + actions);
    }

    /** Restarts after each event, with the coordinator's last effect done and not done; returns the actions taken. */
    private Set<String> restartAtEveryEvent(List<Step> script, boolean hasAnalysts, String selectionJson)
            throws Exception {
        Recorded rec = record(script, hasAnalysts, selectionJson);
        assertEquals("CLOSED", rec.outcome().phase());
        var actions = new HashSet<String>();
        for (boolean effectsDone : new boolean[] {true, false}) {
            for (int written = 1; written <= rec.events().size(); written++) {
                String where = "after event " + written + " (" + rec.events().get(written - 1).type() + "), effects "
                        + (effectsDone ? "done" : "not done");
                Restarted restarted = restartAfter(rec, written, effectsDone, hasAnalysts, selectionJson);
                actions.add(restarted.action());
                assertEndsTheSame(rec, restarted, written, where);
            }
        }
        return actions;
    }

    private void assertEndsTheSame(Recorded rec, Restarted restarted, int written, String where) {
        assertEquals(rec.outcome(), restarted.outcome(), where + ": same final state and answer");
        var events = restarted.events();
        assertTrue(events.stream().allMatch(e -> THREAD.equals(e.threadId())), where + ": no second thread");
        assertEquals(1, count(events, "thread_opened"), where + ": the thread is opened once");
        assertEquals(written < rec.events().size() ? 1 : 0, count(events, "coordinator_resumed"),
                where + ": a takeover is logged once, and not for a thread that had closed");
        assertEquals(1, idsOf(restarted.allPublishes(), "synthesis").size(), where + ": one answer, repeats share an id");
        assertEquals(1, idsOf(restarted.allPublishes(), "thread_close").size(), where + ": one close, repeats share an id");
        assertNoSignalAppliedTwice(events, where);
    }

    private static void assertNoSignalAppliedTwice(List<CoordinatorEvent> events, String where) {
        var seen = new HashSet<String>();
        for (CoordinatorEvent event : events) {
            String messageId = event.data().path("messageId").asText("");
            if (("signal_applied".equals(event.type()) || "failure_recorded".equals(event.type()))
                    && !messageId.isEmpty()) {
                assertTrue(seen.add(messageId), where + ": the signal of message " + messageId + " was applied twice");
            }
        }
    }

    private static long count(List<CoordinatorEvent> events, String type) {
        return events.stream().filter(e -> e.type().equals(type)).count();
    }

    private void selectionIs(String json) {
        selection = json;
    }

    // --- the gap ---

    @Test
    void messagesAgentsSentWhileNoCoordinatorRanAreAppliedOnceAndLogged() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        // Steps 2 to 8 are the agents' messages for the evaluation; the first four are sent while down.
        Restarted restarted = restartAfter(rec, afterEvaluationStarted, true, true, SELECT_THREE, 4, () -> { });

        assertEquals(rec.outcome(), restarted.outcome(), "the thread ends as the uninterrupted run did");
        var down = new ArrayList<String>();
        for (int i = 0; i < 4; i++) {
            // inbound 0 is the thread_start; 1 to 4 are the first four messages after the advisory.
            down.add(parse(rec.inbound().get(i + 1).json()).path("messageId").asText());
        }
        for (String messageId : down) {
            long applied = restarted.events().stream()
                    .filter(e -> messageId.equals(e.data().path("messageId").asText())).count();
            assertEquals(1, applied, "message " + messageId + " is in the log once");
        }
    }

    @Test
    void aLiveMessageForAThreadBeingResumedWaitsForTheGapAndIsThenAppliedOnce() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        String live = "{\"messageId\":\"live-1\",\"threadId\":\"" + THREAD + "\",\"agentName\":\"k8s-workloads\","
                + "\"messageType\":\"agree\",\"content\":\"12 pods\",\"channel\":\"broadcast\","
                + "\"timestamp\":\"" + java.time.Instant.now() + "\"}";
        var held = new boolean[1];

        restartAfter(rec, afterEvaluationStarted, true, true, SELECT_THREE, 0, () -> {
            orchestrator.handleMessage(live);
            held[0] = orchestrator.threadForTest(THREAD) == null;
            orchestrator.handleMessage(live);
        });

        assertTrue(held[0], "the live message did not create or change the thread while its gate was shut");
        long applied = forThread(THREAD).stream().filter(e -> "live-1".equals(e.data().path("messageId").asText())).count();
        assertEquals(1, applied, "the queued message was applied once, and its repeat was dropped");
    }

    @Test
    void aStopSentWhileNoCoordinatorRanMovesTheThreadOnceAndTheContinuationDoesNotStartTheSynthesisAgain()
            throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, afterEvaluationStarted));
        String stop = "{\"messageId\":\"stop-1\",\"threadId\":\"" + THREAD + "\",\"agentName\":\"dashboard\","
                + "\"messageType\":\"stop_requested\",\"content\":\"\",\"channel\":\"broadcast\",\"timestamp\":\""
                + java.time.Instant.now().plusSeconds(5) + "\"}";

        startResume(List.of(new TimedMessage(0, 0, "kubemoot.discuss.ns.crew.broadcast." + THREAD, stop)));
        awaitTypes(THREAD, 1, "thread_closed");
        quiesce();

        assertEquals(1, count(forThread(THREAD), "synthesis_done"), "the answer was written once");
        assertEquals(1, idsOf(published, "synthesis").size());
        assertEquals(1.0, resumeRegistry.get("kubemoot_coordinator_resumes_total")
                .tag("action", ResumeContinuation.MOVED_BY_REPLAY).counter().count());
    }

    @Test
    void aThreadThatCannotBeReadBackIsClosedSoItsAgentsStopAndNoBlankThreadIsMadeOfItsMessages() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, afterEvaluationStarted));
        var coordinatorLog = new FakeBusReader();
        eventLog.asMessages().forEach(m -> coordinatorLog.put(CrewScope.COORDINATOR_STREAM, m));
        BusReader failing = (stream, filter) -> {
            if (CoordinatorResume.DISCUSS_STREAM.equals(stream)) {
                throw new java.io.IOException("stream unavailable");
            }
            return coordinatorLog.readAll(stream, filter);
        };
        var resume = new CoordinatorResume(true, orchestrator, failing, () -> CrewScope.of("ns", "crew"), MAPPER,
                resumeMetrics, System::currentTimeMillis);
        orchestrator.useResume(resume);

        resume.discover();
        resume.launch(Runnable::run);

        List<CoordinatorEvent> events = forThread(THREAD);
        assertEquals("thread_closed", events.get(events.size() - 1).type());
        assertEquals(1, idsOf(published, "thread_close").size(), "its agents are told to stop");
        assertEquals("CLOSED", orchestrator.threadForTest(THREAD).phase.name());
    }

    @Test
    void aThreadAnotherCoordinatorAlreadyTookIsNotTakenOverAndItsGateOpens() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, afterEvaluationStarted));
        var reader = new FakeBusReader();
        eventLog.asMessages().forEach(m -> reader.put(CrewScope.COORDINATOR_STREAM, m));
        var resume = new CoordinatorResume(true, orchestrator, reader, () -> CrewScope.of("ns", "crew"), MAPPER,
                resumeMetrics, System::currentTimeMillis);
        orchestrator.useResume(resume);
        resume.discover();
        // The other coordinator writes after this one read the log.
        eventLog.anotherWriterAppendsTo(rec.subjects().get(0));
        int before = forThread(THREAD).size();

        resume.launch(Runnable::run);

        assertEquals(before, forThread(THREAD).size(), "no coordinator_resumed was written");
        assertNull(orchestrator.threadForTest(THREAD), "the thread is not in this coordinator");
        assertEquals(1.0, resumeRegistry.get("kubemoot_coordinator_resumes_total").tag("action", "fenced")
                .counter().count());
        assertFalse(resume.holdsLive(THREAD, "{}"), "the gate is open");
    }

    @Test
    void ifTheResumesCannotBeScheduledTheGatesStillOpen() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterEvaluationStarted = indexOf(rec.events(), "evaluation_started") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, afterEvaluationStarted));
        var reader = new FakeBusReader();
        eventLog.asMessages().forEach(m -> reader.put(CrewScope.COORDINATOR_STREAM, m));
        var resume = new CoordinatorResume(true, orchestrator, reader, () -> CrewScope.of("ns", "crew"), MAPPER,
                resumeMetrics, System::currentTimeMillis);
        resume.discover();
        assertTrue(resume.holdsLive(THREAD, "{}"), "the gate is shut after the log is read");

        resume.launch(task -> {
            throw new java.util.concurrent.RejectedExecutionException("shut down");
        });

        assertFalse(resume.holdsLive(THREAD, "{}"), "the gate opened, so live messages are not held for ever");
    }

    // --- a log that cannot be resumed from ---

    @Test
    void aThreadWhoseLogIsIncompleteIsClosedNotResumed() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterConvened = indexOf(rec.events(), "subcommittee_convened") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), rec.events().subList(0, afterConvened));
        String random = "{\"messageId\":\"" + java.util.UUID.randomUUID() + "\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"coordinator\",\"messageType\":\"advisory_ready\",\"content\":\"\","
                + "\"channel\":\"broadcast\",\"timestamp\":\"" + java.time.Instant.now() + "\"}";

        startResume(List.of(new TimedMessage(0, 1, "kubemoot.discuss.ns.crew.broadcast." + THREAD, random)));

        List<CoordinatorEvent> events = forThread(THREAD);
        CoordinatorEvent last = events.get(events.size() - 1);
        assertEquals("thread_closed", last.type());
        assertEquals("unlogged", last.data().path("reason").asText());
        assertEquals(0, count(events, "coordinator_resumed"), "the thread was not taken over");
        assertEquals(1, idsOf(published, "thread_close").size(), "its agents are told to stop");
        assertEquals("CLOSED", orchestrator.threadForTest(THREAD).phase.name());
        assertEquals(1.0, resumeRegistry.get("kubemoot_coordinator_resumes_total").tag("action", "abandoned")
                .counter().count(), "the abandoned thread is counted");
    }

    @Test
    void aRequestHandedAgainForAnAbandonedThreadStartsANewOne() throws Exception {
        Recorded rec = record(fullScript(), true, SELECT_THREE);
        int afterConvened = indexOf(rec.events(), "subcommittee_convened") + 1;
        published.clear();
        timeline.clear();
        build(true);
        eventLog.preload(rec.subjects().get(0), withRequestSeq(rec.events().subList(0, afterConvened), 42));
        String random = "{\"messageId\":\"" + java.util.UUID.randomUUID() + "\",\"threadId\":\"" + THREAD
                + "\",\"agentName\":\"coordinator\",\"messageType\":\"advisory\",\"content\":\"\","
                + "\"channel\":\"broadcast\",\"timestamp\":\"" + java.time.Instant.now() + "\"}";
        startResume(List.of(new TimedMessage(0, 1, "kubemoot.discuss.ns.crew.broadcast." + THREAD, random)));

        assertNull(orchestratorResumeMatch(42), "the abandoned thread does not answer for its request");
    }

    // --- flag off ---

    @Test
    void withResumeOffNothingIsReadHeldOrMatched() {
        var off = CoordinatorResume.off();

        off.discover();
        off.launch(Runnable::run);

        assertFalse(off.enabled());
        assertFalse(off.holdsLive("t", "{}"), "no live message is ever held");
        assertNull(off.matchRequest(42), "no request is tied to a thread");
    }

    @Test
    void anOrchestratorBuiltWithoutTheSettingHasResumeOff() throws Exception {
        assertNull(orchestratorResumeMatch(7));
        send(THREAD, "human", "thread_start", "q", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        assertEquals("ADVISORY", orchestrator.threadForTest(THREAD).phase.name(), "a thread starts as it always did");
    }

    private RequestMatch orchestratorResumeMatch(long requestSeq) {
        return orchestrator.resume().matchRequest(requestSeq);
    }

    private static int indexOf(List<CoordinatorEvent> events, String type) {
        for (int i = 0; i < events.size(); i++) {
            if (events.get(i).type().equals(type)) {
                return i;
            }
        }
        throw new AssertionError("no " + type + " in " + events.stream().map(CoordinatorEvent::type).toList());
    }

    private static List<CoordinatorEvent> withRequestSeq(List<CoordinatorEvent> events, long requestSeq) {
        var out = new ArrayList<CoordinatorEvent>();
        for (CoordinatorEvent event : events) {
            if ("thread_opened".equals(event.type())) {
                var data = event.data().deepCopy();
                data.put("requestSeq", requestSeq);
                out.add(new CoordinatorEvent(event.eventId(), event.type(), event.threadId(), event.round(), event.at(),
                        event.coordinator(), data));
            } else {
                out.add(event);
            }
        }
        return out;
    }
}
