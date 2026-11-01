package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.lease.FakeLeaseBucket;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.OrchestrateResult;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Two coordinator replicas over one log, one lease bucket and one discussion stream. Each thread
 * is acted on by the replica that holds its lease and no other; a thread whose owner stopped is
 * taken over by the other replica through resume, with one answer; a request handed to the other
 * replica finds its thread in the log; stop, pause and resume messages are ordered by stream
 * sequence. The model, the bus and the lease bucket are fakes; the orchestrator, the recorder,
 * the reducer, the leases and the resume are real.
 */
class CoordinatorLeaseTest extends CoordinatorThreadTestBase {

    private static final String START_META = "{\"crew\":\"crew\",\"namespace\":\"ns\"}";
    private static final String KEY_PREFIX = "ns.crew.";
    private static final String QUESTION = "How many pods?";
    private static final String ANSWER = "Eleven pods.";

    /** A replica: its orchestrator and the leases and resume that belong to it. */
    private record Replica(String pod, DiscussionOrchestrator orch, CoordinatorLeases leases, CoordinatorResume resume) {
        boolean owns(String threadId) {
            return orch.holdsThread(threadId);
        }
    }

    /** A message an agent put on the discussion stream. */
    private record Said(String subject, String json) {
    }

    private final AtomicLong nanos = new AtomicLong(1);
    private final FakeLeaseBucket bucket = new FakeLeaseBucket(nanos::get, CoordinatorLeases.LEASE.toNanos());
    private final List<Said> stream = Collections.synchronizedList(new ArrayList<>());
    private final ExecutorService callers = Executors.newCachedThreadPool(runnable -> {
        var thread = new Thread(runnable, "orchestrate-caller");
        thread.setDaemon(true);
        return thread;
    });
    private RecordingEventLog sharedLog;

    @Override
    protected boolean withBus() {
        return true;
    }

    @AfterEach
    void stopCallers() {
        callers.shutdownNow();
    }

    // --- replicas ---

    private Replica startReplica(String pod) throws Exception {
        build(false);
        selection = SELECT_TWO;
        if (sharedLog == null) {
            sharedLog = eventLog;
        }
        var orch = orchestrator;
        orch.useEventRecorder(sharedLog.recorder());
        var leases = new CoordinatorLeases(bucket::storeFor, () -> KEY_PREFIX, pod, nanos::get, orch::threadIsOpen,
                orch::threadLost);
        var resume = new CoordinatorResume(true, orch, liveReader(), () -> CrewScope.of("ns", "crew"), MAPPER,
                new DiscussionMetrics(new SimpleMeterRegistry()), System::currentTimeMillis, Duration.ZERO, leases);
        orch.useResume(resume);
        resume.discover();
        return new Replica(pod, orch, leases, resume);
    }

    /** The log and the discussion stream as the replicas find them now: every replica reads the same ones. */
    private BusReader liveReader() {
        return new BusReader() {
            @Override
            public List<BusMessage> readAll(String streamName, String filter) {
                var all = new ArrayList<BusMessage>();
                if (CrewScope.COORDINATOR_STREAM.equals(streamName)) {
                    all.addAll(sharedLog.asMessages());
                } else {
                    long sequence = 0;
                    synchronized (stream) {
                        for (Said said : stream) {
                            all.add(new BusMessage(++sequence, said.subject(), said.json().getBytes()));
                        }
                    }
                    synchronized (published) {
                        for (Published sent : published) {
                            all.add(new BusMessage(++sequence, sent.subject(), sent.body().toString().getBytes()));
                        }
                    }
                }
                return all.stream().filter(m -> FakeBusReader.matches(filter, m.subject())).toList();
            }
        };
    }

    // --- the stream ---

    /** An agent speaks: the message joins the stream and reaches the replicas listed, in that order. */
    private void say(String threadId, String agent, String type, String content, String metadataJson,
                     Instant sentAt, Replica... listeners) throws Exception {
        ObjectNode msg = MAPPER.createObjectNode();
        msg.put("messageId", UUID.randomUUID().toString());
        msg.put("threadId", threadId);
        msg.put("agentName", agent);
        msg.put("messageType", type);
        msg.put("content", content);
        msg.put("channel", "broadcast");
        msg.put("timestamp", sentAt.toString());
        if (metadataJson != null) {
            msg.set("metadata", MAPPER.readTree(metadataJson));
        }
        String json = MAPPER.writeValueAsString(msg);
        long sequence;
        synchronized (stream) {
            stream.add(new Said("kubemoot.discuss.ns.crew.broadcast." + threadId, json));
            sequence = stream.size();
        }
        for (Replica listener : listeners) {
            listener.orch().handleMessage(json, sequence);
        }
    }

    private void say(String threadId, String agent, String type, String content, Replica... listeners)
            throws Exception {
        say(threadId, agent, type, content, null, Instant.now(), listeners);
    }

    private void startExternalThread(String threadId, Replica... listeners) throws Exception {
        say(threadId, "human", "thread_start", QUESTION, START_META, Instant.now(), listeners);
    }

    // --- the log ---

    private List<CoordinatorEvent> eventsOf(String threadId) {
        return sharedLog.events().stream().filter(e -> e.threadId().equals(threadId)).toList();
    }

    private long countOf(String threadId, String type) {
        return eventsOf(threadId).stream().filter(e -> e.type().equals(type)).count();
    }

    private void awaitEvent(String threadId, String type) throws InterruptedException {
        await(() -> countOf(threadId, type) >= 1, "no " + type + " for " + threadId + ", got " + sharedLog.types());
    }

    private CoordinatorThreadState logged(String threadId) {
        return CoordinatorStateReducer.reduce(eventsOf(threadId));
    }

    /** Runs the advisory step on the replica that owns the thread. */
    private void convene(Replica owner, String threadId) throws InterruptedException {
        owner.orch().checkThreadTransition(owner.orch().threadForTest(threadId), Instant.now());
        awaitEvent(threadId, "evaluation_started");
    }

    private void settleAndClose(Replica owner, String threadId) throws InterruptedException {
        owner.orch().checkThreadTransition(owner.orch().threadForTest(threadId), Instant.now().plusSeconds(60));
        awaitEvent(threadId, "thread_closed");
    }

    /**
     * How many times a coordinator published the answer to the thread's broadcast subject. A second
     * replica acting on the same thread would publish it again (with the same derived id, which
     * the stream would drop), so one means exactly one replica acted.
     */
    private int answers() {
        return (int) publishedOfType("synthesis").stream().filter(p -> p.subject().contains(".broadcast.")).count();
    }

    private void advance(Duration time) {
        nanos.addAndGet(time.toNanos());
    }

    private Future<OrchestrateResult> ask(Replica replica, long requestSeq) {
        return callers.submit(() -> replica.orch().orchestrate(QUESTION, "conv-1", "crew", null, requestSeq));
    }

    // --- each thread is acted on by exactly one replica ---

    @Test
    void aThreadStartedOnTheBusIsTakenUpByOneReplicaAndAnsweredOnce() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");

        startExternalThread("t1", a, b);

        assertTrue(a.owns("t1"));
        assertFalse(b.owns("t1"), "the other replica heard the question and left it alone");
        assertEquals("pod-a", bucket.holder(KEY_PREFIX + "t1"));
        convene(a, "t1");
        say("t1", "k8s-metrics", "agree", "3", a, b);
        say("t1", "k8s-workloads", "stand_aside", "nothing to add", a, b);
        settleAndClose(a, "t1");

        assertEquals(1, answers(), "one answer on the stream");
        assertEquals(1, countOf("t1", "thread_opened"));
        assertEquals(ANSWER, logged("t1").answer);
        assertNull(b.orch().threadForTest("t1"), "the other replica never held the thread");
        assertFalse(b.leases().holds("t1"), "and holds no lease for it");
    }

    @Test
    void whicheverReplicaHearsAThreadFirstOwnsItAndTheNextThreadCanGoToTheOther() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");

        startExternalThread("t1", a, b);
        startExternalThread("t2", b, a);

        assertTrue(a.owns("t1") && !b.owns("t1"));
        assertTrue(b.owns("t2") && !a.owns("t2"));
        assertEquals("pod-a", bucket.holder(KEY_PREFIX + "t1"));
        assertEquals("pod-b", bucket.holder(KEY_PREFIX + "t2"));
    }

    @Test
    void aMessageForAThreadNoReplicaOwnsIsDroppedNotMadeIntoABlankThread() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");

        say("ghost", "k8s-metrics", "agree", "3", a, b);

        assertNull(a.orch().threadForTest("ghost"));
        assertNull(b.orch().threadForTest("ghost"));
    }

    @Test
    void aClosedThreadsLeaseIsGivenUpSoNoReplicaKeepsRenewingIt() throws Exception {
        var a = startReplica("pod-a");
        startExternalThread("t1", a);
        convene(a, "t1");
        say("t1", "k8s-metrics", "agree", "3", a);
        say("t1", "k8s-workloads", "stand_aside", "nothing", a);
        settleAndClose(a, "t1");

        await(() -> bucket.holder(KEY_PREFIX + "t1") == null, "the lease was not given up at close");
    }

    // --- a lease expiry hands the thread over ---

    @Test
    void aThreadWhoseOwnerStoppedIsTakenOverByTheOtherReplicaAndAnsweredOnce() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");
        say("t1", "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}", Instant.now(), a, b);

        // The owner stops. The agents finish and say so; only the other replica is listening, and it
        // does not own the thread yet, so it drops the messages (they stay on the stream).
        advance(CoordinatorLeases.LEASE);
        say("t1", "k8s-metrics", "agree", "3", b);
        say("t1", "k8s-workloads", "stand_aside", "nothing to add", b);
        assertFalse(b.owns("t1"));
        b.resume().scan(Runnable::run);

        assertTrue(b.owns("t1"), "the lease lapsed, so the scan took the thread");
        assertEquals("pod-b", bucket.holder(KEY_PREFIX + "t1"));
        assertEquals(1, countOf("t1", "coordinator_resumed"));
        settleAndClose(b, "t1");
        var outcome = logged("t1");
        assertEquals(ANSWER, outcome.answer);
        assertEquals(java.util.Set.of("k8s-metrics"), outcome.agree.keySet(), "the answer the agent gave while no one owned the thread");
        assertEquals(java.util.Set.of("k8s-workloads"), outcome.standAside);
        assertEquals(1, answers(), "one answer");
        assertEquals(1, countOf("t1", "thread_opened"));
    }

    @Test
    void aThreadWhoseOwnerIsStillRenewingIsNotTakenOver() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");

        advance(CoordinatorLeases.RENEW_EVERY);
        a.leases().tick();
        advance(CoordinatorLeases.RENEW_EVERY);
        a.leases().tick();
        advance(CoordinatorLeases.RENEW_EVERY);
        a.leases().tick();
        b.resume().scan(Runnable::run);

        assertFalse(b.owns("t1"));
        assertTrue(a.owns("t1"));
        assertEquals(0, countOf("t1", "coordinator_resumed"));
    }

    @Test
    void aDeposedOwnerLearnsItAtItsNextRenewalAndActsOnNothingMore() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");
        advance(CoordinatorLeases.LEASE);
        b.resume().scan(Runnable::run);
        assertTrue(b.owns("t1"));
        var stale = a.orch().threadForTest("t1");
        int eventsBefore = sharedLog.events().size();

        a.leases().tick();

        assertFalse(a.owns("t1"), "forgotten");
        a.orch().checkThreadTransition(stale, Instant.now().plusSeconds(120));
        assertEquals(eventsBefore, sharedLog.events().size(), "the deposed replica wrote nothing");
        assertEquals(0, answers());
    }

    @Test
    void aDeposedOwnerThatDoesNotKnowItYetIsStoppedByTheFencedAppend() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");
        say("t1", "k8s-metrics", "agree", "3", a, b);
        say("t1", "k8s-workloads", "stand_aside", "nothing", a, b);
        advance(CoordinatorLeases.LEASE);
        b.resume().scan(Runnable::run);
        var stale = a.orch().threadForTest("t1");

        // A has not ticked, so it still thinks it owns the thread; its first append is refused.
        a.orch().checkThreadTransition(stale, Instant.now().plusSeconds(60));
        settleAndClose(b, "t1");

        assertEquals(1, answers(), "only the owner answered");
        assertTrue(a.orch().recorder().fenced(stale));
    }

    // --- a request handed to the other replica finds its thread ---

    @Test
    void aRequestHandedToTheOtherReplicaAfterTheOwnerStoppedFindsItsThreadInTheLog() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        Future<OrchestrateResult> first = ask(a, 42);
        await(() -> !sharedLog.types().isEmpty() && sharedLog.types().contains("phase_entered"),
                "the thread was not started: " + sharedLog.types());
        String threadId = sharedLog.events().get(0).threadId();
        first.cancel(true);
        advance(CoordinatorLeases.LEASE);

        Future<OrchestrateResult> second = ask(b, 42);
        await(() -> b.owns(threadId), "the request did not take the thread over");
        convene(b, threadId);
        say(threadId, "k8s-metrics", "agree", "3", b);
        say(threadId, "k8s-workloads", "stand_aside", "nothing to add", b);
        settleAndClose(b, threadId);

        OrchestrateResult result = second.get(20, TimeUnit.SECONDS);
        assertEquals(threadId, result.threadId(), "the request got the thread it had started");
        assertEquals(ANSWER, result.response());
        assertEquals(1, sharedLog.events().stream().filter(e -> e.type().equals("thread_opened")).count(),
                "no second thread was opened");
        assertEquals(1, answers());
        assertEquals(1, publishedOfType("thread_start").size(), "the question was not published again");
    }

    @Test
    void aRequestHandedToTheOtherReplicaWhileTheOwnerLivesWaitsForTheOwnersAnswer() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        Future<OrchestrateResult> first = ask(a, 42);
        await(() -> sharedLog.types().contains("phase_entered"), "the thread was not started: " + sharedLog.types());
        String threadId = sharedLog.events().get(0).threadId();

        Future<OrchestrateResult> second = ask(b, 42);
        convene(a, threadId);
        say(threadId, "k8s-metrics", "agree", "3", a, b);
        say(threadId, "k8s-workloads", "stand_aside", "nothing to add", a, b);
        settleAndClose(a, threadId);

        assertEquals(ANSWER, first.get(20, TimeUnit.SECONDS).response());
        OrchestrateResult result = second.get(20, TimeUnit.SECONDS);
        assertEquals(threadId, result.threadId());
        assertEquals(ANSWER, result.response(), "the logged answer");
        assertFalse(b.owns(threadId), "the other replica never took the thread");
        assertEquals(1, sharedLog.events().stream().filter(e -> e.type().equals("thread_opened")).count());
        assertEquals(1, answers());
    }

    @Test
    void aRequestWhoseThreadWasLostToTheOtherReplicaFollowsItAndIsAnsweredFromIt() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        Future<OrchestrateResult> first = ask(a, 42);
        await(() -> sharedLog.types().contains("phase_entered"), "the thread was not started: " + sharedLog.types());
        String threadId = sharedLog.events().get(0).threadId();
        convene(a, threadId);

        // A is cut off for longer than the lease: B takes the thread, A learns it at its next renewal.
        advance(CoordinatorLeases.LEASE);
        b.resume().scan(Runnable::run);
        assertTrue(b.owns(threadId));
        a.leases().tick();
        say(threadId, "k8s-metrics", "agree", "3", b);
        say(threadId, "k8s-workloads", "stand_aside", "nothing to add", b);
        settleAndClose(b, threadId);

        OrchestrateResult result = first.get(30, TimeUnit.SECONDS);
        assertEquals(threadId, result.threadId());
        assertEquals(ANSWER, result.response(), "A's request is answered with B's answer, not dropped or answered twice");
        assertEquals(1, answers());
        assertEquals(1, sharedLog.events().stream().filter(e -> e.type().equals("thread_opened")).count());
    }

    // --- stop, pause and resume are ordered by stream sequence ---

    @Test
    void aPauseSentWhileNoReplicaOwnedTheThreadIsAppliedWhateverTheSendersClockSays() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");
        say("t1", "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}", Instant.now(), a, b);

        advance(CoordinatorLeases.LEASE);
        // The dashboard's clock is an hour behind: stamped before the last event, but after it in the stream.
        say("t1", "dashboard", "thread_pause", "", null, Instant.now().minus(Duration.ofHours(1)), b);
        b.resume().scan(Runnable::run);

        assertTrue(b.owns("t1"));
        assertEquals(Phase.PAUSED, b.orch().threadForTest("t1").phase);
    }

    @Test
    void aPauseTheOldOwnerAlreadyHonoredIsNotAppliedAgainWhateverTheSendersClockSays() throws Exception {
        var a = startReplica("pod-a");
        var b = startReplica("pod-b");
        startExternalThread("t1", a, b);
        convene(a, "t1");
        // The dashboard's clock is an hour ahead; the owner paused and resumed the thread on these.
        say("t1", "dashboard", "thread_pause", "", null, Instant.now().plus(Duration.ofHours(1)), a, b);
        assertEquals(Phase.PAUSED, a.orch().threadForTest("t1").phase);
        say("t1", "dashboard", "thread_resume", "", null, Instant.now().plus(Duration.ofHours(1)), a, b);
        assertEquals(Phase.EVALUATING, a.orch().threadForTest("t1").phase);

        advance(CoordinatorLeases.LEASE);
        b.resume().scan(Runnable::run);

        assertTrue(b.owns("t1"));
        assertEquals(Phase.EVALUATING, b.orch().threadForTest("t1").phase,
                "the pause is accounted for in the log, so it is not replayed on top of the resume");
    }

    @Test
    void everyEventTheOwnerWritesStatesTheStreamSequenceItHadApplied() throws Exception {
        var a = startReplica("pod-a");
        startExternalThread("t1", a);
        convene(a, "t1");
        say("t1", "k8s-metrics", "evaluating", "", "{\"gpuLabel\":\"rig0\"}", Instant.now(), a);

        assertEquals(2, logged("t1").discussSeq, "the last message applied before the last event");
        assertNotNull(eventsOf("t1").get(0).data().get("discussSeq"));
        assertEquals(List.of(), eventsOf("t1").stream().filter(e -> e.data().path("discussSeq").asLong() <= 0).toList());
    }

    @Test
    void aThreadBeingTakenOverKeepsItsLeaseThroughTheRenewalPassesBeforeItIsRegistered() throws Exception {
        var a = startReplica("pod-a");
        startExternalThread("t1", a);
        convene(a, "t1");
        advance(CoordinatorLeases.LEASE);

        // The replica starts, reads the log and wins the lease; the takeover has not run yet.
        var b = startReplica("pod-b");
        assertFalse(b.owns("t1"), "not registered yet");
        assertTrue(b.orch().threadIsOpen("t1"), "but being taken over counts as open");
        for (int i = 0; i < 4; i++) {
            advance(CoordinatorLeases.RENEW_EVERY);
            b.leases().tick();
        }

        assertEquals("pod-b", bucket.holder(KEY_PREFIX + "t1"), "the lease outlived the passes");
        assertFalse(startReplica("pod-c").resume().isResuming("t1"), "and nobody else took it");
    }

    @Test
    void aControlIsStampedOnlyAfterItsEffectIsDecidedSoNoEventClaimsMoreThanItAccountsFor() throws Exception {
        var a = startReplica("pod-a");
        startExternalThread("t1", a);
        convene(a, "t1");
        say("t1", "dashboard", "thread_pause", "", a);
        long pauseSeq = stream.size();
        say("t1", "dashboard", "thread_resume", "", a);

        var phaseEvents = eventsOf("t1").stream().filter(e -> e.type().equals("phase_entered")).toList();
        var paused = phaseEvents.stream().filter(e -> "pause".equals(e.data().path("reason").asText())).findFirst().orElseThrow();
        var resumed = phaseEvents.stream().filter(e -> "resume".equals(e.data().path("reason").asText())).findFirst().orElseThrow();
        assertTrue(paused.data().path("discussSeq").asLong() < pauseSeq, "the pause's own event does not claim the pause");
        assertEquals(pauseSeq, resumed.data().path("discussSeq").asLong(), "the next event does");
    }
}
