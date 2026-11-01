package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.OrchestrateResult;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * A request taken from the queue is tied to its thread by the request's stream sequence, so
 * a coordinator that restarts and is handed the same request again picks that thread up
 * instead of starting another.
 */
class CoordinatorResumeRequestTest extends ResumeRestartHarness {

    private static final String QUESTION = "How many pods?";
    private static final String CONVERSATION = "conv-1";
    private static final long REQUEST_SEQ = 42;

    private final ExecutorService callers = Executors.newCachedThreadPool(runnable -> {
        var thread = new Thread(runnable, "orchestrate-caller");
        thread.setDaemon(true);
        return thread;
    });

    @AfterEach
    void stopCallers() {
        callers.shutdownNow();
    }

    private Future<OrchestrateResult> ask(long requestSeq) {
        return callers.submit(() -> orchestrator.orchestrate(QUESTION, CONVERSATION, "crew", null, requestSeq));
    }

    /** The first coordinator takes a request, opens its thread, and stops. Returns the thread's id. */
    private String firstCoordinatorTakesTheRequestAndStops(List<CoordinatorEvent> written, List<Published> sent)
            throws Exception {
        build(false);
        selection = SELECT_TWO;
        resumeIsOn();
        Future<OrchestrateResult> first = ask(REQUEST_SEQ);
        await(() -> eventLog.types().contains("phase_entered"), "the thread was not started: " + eventLog.types());
        first.cancel(true);
        written.addAll(eventLog.events());
        sent.addAll(published);
        return written.get(0).threadId();
    }

    /** Resume is on for this coordinator, with no earlier log to read. */
    private void resumeIsOn() {
        var resume = new CoordinatorResume(true, orchestrator, new FakeBusReader(), () -> CrewScope.of("ns", "crew"),
                MAPPER, resumeMetrics, System::currentTimeMillis);
        orchestrator.useResume(resume);
        resume.discover();
    }

    private void secondCoordinatorStarts(List<CoordinatorEvent> written, List<Published> sent) throws Exception {
        String subject = eventLog.subjects().get(0);
        published.clear();
        timeline.clear();
        inbound.clear();
        build(false);
        selection = SELECT_TWO;
        eventLog.preload(subject, written);
        var bus = new ArrayList<TimedMessage>();
        for (int i = 0; i < sent.size(); i++) {
            bus.add(new TimedMessage(i, 1, sent.get(i).subject(), sent.get(i).body().toString()));
        }
        startResume(bus);
    }

    @Test
    void aRequestHandedAgainAfterARestartPicksUpItsThreadInsteadOfStartingAnother() throws Exception {
        var written = new ArrayList<CoordinatorEvent>();
        var sent = new ArrayList<Published>();
        String threadId = firstCoordinatorTakesTheRequestAndStops(written, sent);
        assertEquals(REQUEST_SEQ, written.get(0).data().path("requestSeq").asLong(),
                "the thread's first event names the request that started it");
        secondCoordinatorStarts(written, sent);

        Future<OrchestrateResult> second = ask(REQUEST_SEQ);
        var thread = orchestrator.threadForTest(threadId);
        orchestrator.checkThreadTransition(thread, Instant.now());
        awaitTypes(threadId, 1, "evaluation_started");
        send(threadId, "k8s-metrics", "agree", "3", null);
        send(threadId, "k8s-workloads", "stand_aside", "nothing to add", null);
        orchestrator.checkThreadTransition(thread, Instant.now().plusSeconds(60));
        awaitTypes(threadId, 1, "thread_closed");

        OrchestrateResult result = second.get(20, TimeUnit.SECONDS);
        assertEquals(threadId, result.threadId(), "the request got the thread it had started");
        assertEquals("Eleven pods.", result.response());
        var events = eventLog.events();
        assertTrue(events.stream().allMatch(e -> e.threadId().equals(threadId)), "no second thread was opened");
        assertEquals(1, events.stream().filter(e -> e.type().equals("thread_opened")).count());
        assertEquals(1, events.stream().filter(e -> e.type().equals("coordinator_resumed")).count());
        assertTrue(publishedOfType("thread_start").isEmpty(), "the question was not published again");
    }

    @Test
    void aRequestHandedAgainAfterItsThreadClosedGetsTheLoggedAnswerAndTheTurnIsRecordedOnce() throws Exception {
        var written = new ArrayList<CoordinatorEvent>();
        var sent = new ArrayList<Published>();
        String threadId = firstCoordinatorTakesTheRequestAndStops(written, sent);
        secondCoordinatorStarts(written, sent);
        Future<OrchestrateResult> second = ask(REQUEST_SEQ);
        var thread = orchestrator.threadForTest(threadId);
        orchestrator.checkThreadTransition(thread, Instant.now());
        awaitTypes(threadId, 1, "evaluation_started");
        send(threadId, "k8s-metrics", "agree", "3", null);
        send(threadId, "k8s-workloads", "stand_aside", "nothing to add", null);
        orchestrator.checkThreadTransition(thread, Instant.now().plusSeconds(60));
        second.get(20, TimeUnit.SECONDS);

        // A third coordinator starts after the close; the request is handed again before it was acknowledged.
        var closedLog = new ArrayList<>(eventLog.events());
        var closedSent = new ArrayList<>(published);
        secondCoordinatorStarts(closedLog, closedSent);
        int eventsBefore = eventLog.events().size();
        int publishedBefore = published.size();

        OrchestrateResult again = ask(REQUEST_SEQ).get(20, TimeUnit.SECONDS);
        OrchestrateResult andAgain = ask(REQUEST_SEQ).get(20, TimeUnit.SECONDS);

        assertEquals(threadId, again.threadId());
        assertEquals("Eleven pods.", again.response(), "the logged answer");
        assertEquals(andAgain, again);
        assertEquals(eventsBefore, eventLog.events().size(), "nothing more was logged");
        assertEquals(publishedBefore, published.size(), "nothing more was published");
        var state = new DiscussionOrchestrator.ThreadState("probe");
        state.conversationId = CONVERSATION;
        assertEquals(1, ((List<?>) orchestrator.advisoryReadyMetadata(state, List.of(), "")
                .get("conversationContext")).size(), "the turn is in the conversation once");
    }

    @Test
    void withTheLogOnAndResumeOffTheQuestionAndTheOpeningAreAsTheyWereBeforeResume() throws Exception {
        build(false);
        published.clear();

        ask(REQUEST_SEQ);
        await(() -> publishedOfType("thread_start").size() == 1, "the thread was started");

        Published start = publishedOfType("thread_start").get(0);
        assertNull(start.streamId(), "no stream-level id");
        assertFalse(start.messageId().contains("/msg/"), "a random id, not one made from the log");
        assertFalse(eventLog.events().get(0).data().has("requestSeq"), "the request is not recorded");
    }

    @Test
    void withResumeOffTheSameRequestStartsANewThreadEachTimeAndThreadStartIsAsItAlwaysWas() throws Exception {
        build(false);
        orchestrator.useEventRecorder(CoordinatorEventRecorder.off());
        published.clear();

        ask(7);
        ask(7);
        await(() -> publishedOfType("thread_start").size() == 2, "two threads were started");

        var starts = publishedOfType("thread_start");
        assertNotEquals(starts.get(0).body().path("threadId").asText(), starts.get(1).body().path("threadId").asText(),
                "the request was not tied to a thread");
        for (Published start : starts) {
            assertNull(start.streamId(), "no stream-level id is attached");
            assertFalse(start.messageId().contains("/msg/"), "the message id is random, not derived from a log event");
        }
    }
}
