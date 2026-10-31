package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * The coordinator writes ahead: a decision's event is in the log before its effect is on
 * the bus; the effects carry ids derived from the events; a writer another coordinator
 * outran stops acting; and a closed thread's log is checked against its live state. The
 * real orchestrator runs against a fake model, a fake log and a fake bus.
 */
class CoordinatorWriteAheadTest extends CoordinatorThreadTestBase {

    private static final String CLOSE_BY_DIRECT_ANSWER = "thread_closed";

    @Override
    protected boolean withBus() {
        return true;
    }

    private static String subject(String threadId) {
        return "kubemoot.coordinator.ns.crew." + threadId;
    }

    private void start(String t) throws Exception {
        send(t, "human", "thread_start", "How many pods run?", "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
    }

    /** A thread that goes through advisory, evaluation, review and synthesis to a closed thread. */
    private void runFullThread(String t) throws Exception {
        start(t);
        advanceThroughAdvisory(t);
        send(t, "k8s-metrics", "agree", "11 pods", null);
        send(t, "k8s-workloads", "agree", "12 pods", null);
        settle(t, EVALUATION_TO_REVIEW);
        send(t, "k8s-analyst", "agree", "checked", null);
        settle(t, REVIEW_TO_SYNTHESIS);
        awaitTypes(t, 1, CLOSE_BY_DIRECT_ANSWER);
    }

    /** A thread the coordinator answers itself: the advisory step ends in a close. */
    private void runDirectThread(String t) throws Exception {
        selection = SELECT_NONE;
        start(t);
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitTypes(t, 1, CLOSE_BY_DIRECT_ANSWER);
    }

    private int at(String entry) {
        int index = timeline.indexOf(entry);
        assertTrue(index >= 0, entry + " never happened: " + timeline);
        return index;
    }

    // --- 1. write-ahead order ---

    @Test
    void everyEffectIsPublishedAfterTheEventThatDecidedIt() throws Exception {
        runFullThread("t-order");

        assertTrue(at("event:subcommittee_convened") < at("bus:advisory"), "the brief");
        assertTrue(at("event:subcommittee_convened") < at("bus:subcommittee"), "the subcommittee line");
        assertTrue(at("event:evaluation_started") < at("bus:advisory_ready"), "the evaluation signal");
        assertTrue(at("event:review_started") < at("bus:review_ready"), "the review signal");
        assertTrue(at("event:synthesis_done") < at("bus:synthesis"), "the answer is in the log first");
        assertTrue(at("bus:synthesis") < at("bus:thread_close"));
        assertTrue(at("bus:thread_close") < at("event:thread_closed"));
    }

    @Test
    void theAnswerInTheLogIsTheAnswerOnTheBus() throws Exception {
        runFullThread("t-answer");

        var logged = eventLog.events().stream().filter(e -> e.type().equals("synthesis_done")).findFirst()
                .orElseThrow().data().path("response").asText();
        assertEquals("Eleven pods.", logged);
        assertEquals(logged, publishedOfType("synthesis").get(0).body().path("content").asText());
    }

    @Test
    void aFailedAppendOfTheBriefPublishesNoBriefAndDoesNotTellTheToolers() throws Exception {
        eventLog.failEventsOfType("subcommittee_convened");
        start("t-brief");
        orchestrator.checkThreadTransition(orchestrator.threadForTest("t-brief"), Instant.now());
        awaitRefused("subcommittee_convened");
        Thread.sleep(200);

        assertTrue(publishedOfType("advisory").isEmpty(), "no brief");
        assertTrue(publishedOfType("subcommittee").isEmpty());
        assertTrue(publishedOfType("advisory_ready").isEmpty(), "no signal to the toolers");
        assertFalse(eventLog.types().contains("evaluation_started"));
        assertEquals(1, eventLog.counted("kubemoot_coordinator_log_appends_total", "lost"));
    }

    @Test
    void aFailedAppendOfTheRosterPublishesNoEvaluationSignal() throws Exception {
        eventLog.failEventsOfType("evaluation_started");
        start("t-ready");
        orchestrator.checkThreadTransition(orchestrator.threadForTest("t-ready"), Instant.now());
        awaitRefused("evaluation_started");
        Thread.sleep(200);

        assertTrue(publishedOfType("advisory_ready").isEmpty());
        assertFalse(publishedOfType("advisory").isEmpty(), "the brief was logged, so it went out");
    }

    @Test
    void aFailedAppendOfTheReviewRosterPublishesNoReviewSignalButTheThreadStillFinishes() throws Exception {
        String t = "t-review";
        start(t);
        advanceThroughAdvisory(t);
        eventLog.failEventsOfType("review_started");
        send(t, "k8s-metrics", "agree", "11 pods", null);
        send(t, "k8s-workloads", "agree", "12 pods", null);

        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now().plusSeconds(60));
        awaitRefused("review_started");

        assertTrue(publishedOfType("review_ready").isEmpty());
        assertEquals("REVIEW", orchestrator.threadForTest(t).phase.name(), "the phase machine still moved on");
    }

    @Test
    void aFailedAppendOfTheAnswerPublishesNoAnswerButTheThreadStillEndsAndTheAskerGetsIt() throws Exception {
        eventLog.failEventsOfType("synthesis_done");
        var asker = orchestrator.registerPendingForTest("t-nolog");

        runDirectThread("t-nolog");

        assertTrue(publishedOfType("synthesis").isEmpty(), "nothing is on the bus that is not in the log");
        assertEquals(1, publishedOfType("thread_close").stream().map(Published::messageId).distinct().count(),
                "thread_close still goes out so agents stop heartbeating on a thread that is over");
        assertEquals("Four.", asker.get(5, java.util.concurrent.TimeUnit.SECONDS), "the asker gets the answer");
        assertEquals("CLOSED", orchestrator.threadForTest("t-nolog").phase.name(), "the thread is never failed over the log");
    }

    // --- 2. derived message ids ---

    @Test
    void theEffectsCarryIdsDerivedFromTheirEventsAndTheStreamIdNamesTheSubject() throws Exception {
        String t = "t-ids";
        runFullThread(t);

        var synthesis = publishedOfType("synthesis");
        assertEquals(t + "/0/synthesis/done/1/msg/synthesis", synthesis.get(0).messageId());
        assertEquals(t + "/0/closed/msg/thread_close", publishedOfType("thread_close").get(0).messageId());
        assertEquals(t + "/0/evaluation/1/msg/advisory_ready", publishedOfType("advisory_ready").get(0).messageId());
        assertEquals(t + "/0/review/1/msg/review_ready", publishedOfType("review_ready").get(0).messageId());
        assertEquals(t + "/0/subcommittee/1/msg/advisory", publishedOfType("advisory").get(0).messageId());
        synchronized (published) {
            for (var p : published) {
                assertEquals(p.messageId() + "@" + p.subject(), p.streamId(), p.type());
            }
        }
        assertEquals(synthesis.size(), synthesis.stream().map(Published::streamId).distinct().count(),
                "a message sent to two subjects has a different stream id on each, or the stream would drop the second");
    }

    @Test
    void aDecisionCarriedOutTwiceIsOneMessageToTheStream() throws Exception {
        String t = "t-dedupe";
        runFullThread(t);
        var first = publishedOfType("thread_close");
        String channel = first.get(0).body().path("channel").asText();

        orchestrator.publishThreadClose(t, channel);

        var all = publishedOfType("thread_close");
        assertEquals(first.size() * 2, all.size(), "the repeat was sent");
        assertEquals(new HashSet<>(first.stream().map(Published::streamId).toList()),
                new HashSet<>(all.stream().map(Published::streamId).toList()),
                "the repeat has the stream ids of the first send, so the stream stores nothing new");
        assertEquals(first.get(0).messageId(), all.get(all.size() - 1).messageId(), "consumers drop the repeated messageId");
    }

    @Test
    void withTheLogOffMessagesGetRandomIdsAndNoStreamId() throws Exception {
        orchestrator.useEventRecorder(CoordinatorEventRecorder.off());
        selection = SELECT_NONE;
        start("t-off");
        orchestrator.checkThreadTransition(orchestrator.threadForTest("t-off"), Instant.now());
        awaitPublished("thread_close");

        assertFalse(published.isEmpty());
        synchronized (published) {
            for (var p : published) {
                assertNull(p.streamId(), p.type() + " is published as before");
                assertFalse(p.messageId().contains("/"), p.type() + " keeps a random id");
            }
        }
    }

    // --- 3. fenced appends ---

    @Test
    void aCoordinatorAnotherOneOutranStopsActingOnTheThread() throws Exception {
        String t = "t-fence";
        eventLog.afterAppendOf("subcommittee_convened", () -> eventLog.anotherWriterAppendsTo(subject(t)));

        runFullThreadUntilFenced(t);

        var state = orchestrator.threadForTest(t);
        assertTrue(state.trace.fenced());
        assertNotEquals("CLOSED", state.phase.name());
        assertTrue(publishedOfType("synthesis").isEmpty(), "a deposed coordinator publishes no answer");
        assertTrue(publishedOfType("thread_close").isEmpty());
        assertEquals(1, eventLog.counted("kubemoot_coordinator_log_appends_total", "fenced"));
    }

    @Test
    void aFencedThreadReleasesTheAskerWithoutAnAnswer() throws Exception {
        String t = "t-fence-asker";
        var asker = orchestrator.registerPendingForTest(t);
        eventLog.afterAppendOf("subcommittee_convened", () -> eventLog.anotherWriterAppendsTo(subject(t)));

        runFullThreadUntilFenced(t);

        assertNull(asker.get(5, java.util.concurrent.TimeUnit.SECONDS), "the other coordinator owns the answer");
    }

    @Test
    void aFencedZombieIsEvictedWithoutPublishingAndReleasesTheAsker() throws Exception {
        String t = "t-fence-zombie";
        start(t);
        var asker = orchestrator.registerPendingForTest(t);
        eventLog.anotherWriterAppendsTo(subject(t));
        send(t, "k8s-metrics", "evaluating", "", null);
        int bus = published.size();
        var evicted = new java.util.ArrayList<String>();

        assertTrue(orchestrator.forceCloseZombie(orchestrator.threadForTest(t), 4000, evicted));

        assertEquals(List.of(t), evicted);
        assertEquals(bus, published.size());
        assertNull(asker.get(5, java.util.concurrent.TimeUnit.SECONDS));
    }

    @Test
    void aFencedThreadIsNotDrivenAndItsMessagesAreNotRead() throws Exception {
        String t = "t-fence2";
        start(t);
        eventLog.anotherWriterAppendsTo(subject(t));
        send(t, "k8s-metrics", "evaluating", "", null);
        var state = orchestrator.threadForTest(t);
        assertTrue(state.trace.fenced(), "the next append was refused");
        int events = eventLog.events().size();
        int bus = published.size();

        send(t, "k8s-metrics", "agree", "11 pods", null);
        orchestrator.checkThreadTransition(state, Instant.now().plusSeconds(600));

        assertTrue(state.agreeSignals.isEmpty(), "messages for a fenced thread are not applied");
        assertEquals(events, eventLog.events().size(), "nothing more is written");
        assertEquals(bus, published.size(), "nothing more is published");
    }

    @Test
    void theFenceCheckStatesTheLastSequenceTheWriterSaw() throws Exception {
        String t = "t-seq";
        runFullThread(t);

        List<Long> expected = eventLog.expectedSequences();
        for (int i = 0; i < expected.size(); i++) {
            assertEquals(i, expected.get(i), "append #" + i + " states the sequence of the one before it");
        }
        assertEquals(expected.size(), eventLog.lastSequence(subject(t)));
    }

    // --- 4. the check at close ---

    @Test
    void aThreadWhoseLogRebuildsItIsCountedAsAMatch() throws Exception {
        runFullThread("t-match");
        awaitChecked("match");

        assertEquals(1, eventLog.counted("kubemoot_coordinator_log_checks_total", "match"));
        assertEquals(0, eventLog.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
    }

    @Test
    void aThreadWhoseLogLostAnEventIsCountedAsAMismatchAndStillCloses() throws Exception {
        eventLog.failEventsOfType("signal_applied");

        runFullThread("t-mismatch");
        awaitChecked("mismatch");

        assertEquals(1, eventLog.counted("kubemoot_coordinator_log_checks_total", "mismatch"));
        assertEquals(0, eventLog.counted("kubemoot_coordinator_log_checks_total", "match"));
        assertEquals("CLOSED", orchestrator.threadForTest("t-mismatch").phase.name());
        assertFalse(publishedOfType("synthesis").isEmpty(), "a mismatch never fails the thread");
    }

    @Test
    void aDirectAnswerIsChecked() throws Exception {
        runDirectThread("t-direct-check");
        awaitChecked("match");

        assertEquals(1, eventLog.counted("kubemoot_coordinator_log_checks_total", "match"));
    }

    // --- helpers ---

    private void runFullThreadUntilFenced(String t) throws Exception {
        selection = SELECT_NONE;
        start(t);
        orchestrator.checkThreadTransition(orchestrator.threadForTest(t), Instant.now());
        awaitPublished("advisory");
        Thread.sleep(300);
    }

    /** The check at close runs just after the close event is written. */
    private void awaitChecked(String result) throws InterruptedException {
        await(() -> eventLog.counted("kubemoot_coordinator_log_checks_total", result) >= 1, "no " + result + " check");
    }

    private void awaitRefused(String type) throws InterruptedException {
        await(() -> eventLog.refusedTypes().contains(type), "no refused " + type);
    }

    private void awaitPublished(String type) throws InterruptedException {
        await(() -> !publishedOfType(type).isEmpty(), "nothing published as " + type + ", got " + timeline);
    }
}
