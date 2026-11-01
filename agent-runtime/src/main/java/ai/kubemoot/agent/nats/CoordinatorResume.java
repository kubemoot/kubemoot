package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.BusReader.BusMessage;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.eclipse.microprofile.config.ConfigProvider;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.io.IOException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executor;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.function.Supplier;

/**
 * A restarted coordinator picks up the threads the last one left in flight.
 *
 * <p><b>At start</b> ({@link #discover}) it reads its crew's coordinator log, reduces each
 * thread's events to the state they describe, and sorts the threads: one that closed is only
 * remembered (so a request handed again can be answered from its logged answer), one that is
 * still open is a candidate. Each candidate gets a gate before the live subscription starts, so
 * live messages for it wait.
 *
 * <p><b>Then, for each candidate</b> ({@link #launch}) it reads the thread's messages back
 * from the discussion stream. A thread whose log is incomplete (the coordinator published an
 * effect with no logged decision behind it, which is what a failed append leaves) is not
 * resumed: it is closed, because no coordinator will conclude it and its agents would work for
 * nobody, and the request that started it, handed again, starts a new thread. Otherwise the
 * thread's state is rebuilt, the takeover is logged ({@code coordinator_resumed}), the agents
 * still pending get fresh deadlines, the messages the log has not seen are applied through the
 * handlers the live path uses, the thread joins the phase sweep, {@link ResumeContinuation}
 * carries out what the last decision left undone, and the gate opens.
 *
 * <p>A single coordinator replica is assumed; the log's fenced appends keep a second writer from
 * acting on a thread, but nothing here decides which replica owns a thread.
 */
final class CoordinatorResume {

    /** The setting that turns resume on: environment variable {@code KUBEMOOT_COORDINATOR_RESUME}. */
    static final String CONFIG_KEY = "kubemoot.coordinator.resume";
    static final String DISCUSS_STREAM = "KUBEMOOT_DISCUSS";

    private static final Logger log = LoggerFactory.getLogger(CoordinatorResume.class);
    /** How long a request waits for the start-up read of the log before it is treated as new. */
    private static final long READY_WAIT_SECONDS = 60;
    /** How long a request waits for the thread it names to be rebuilt. */
    private static final long REBUILD_WAIT_SECONDS = 120;
    /** Closed threads remembered by request, so a request handed again after its close is answered, not rerun. */
    private static final int MAX_CLOSED_REMEMBERED = 500;
    private static final String CLOSE_UNLOGGED = "unlogged";
    /** The messages the coordinator publishes about a thread; with the log on each carries an id made from its event. */
    private static final Set<String> COORDINATOR_MESSAGES = Set.of(DiscussionOrchestrator.MSG_THREAD_START,
            DiscussionOrchestrator.MSG_ADVISORY, SubcommitteeLine.MESSAGE_TYPE, DiscussionOrchestrator.MSG_ADVISORY_READY,
            DiscussionOrchestrator.MSG_REVIEW_READY, DiscussionOrchestrator.MSG_SYNTHESIS,
            DiscussionOrchestrator.MSG_THREAD_CLOSE);

    private record Candidate(CoordinatorThreadState state, List<CoordinatorEvent> events, long lastSequence) {
    }

    private record ClosedThread(String threadId, String answer) {
    }

    private final boolean enabled;
    private final DiscussionOrchestrator orchestrator;
    private final BusReader reader;
    private final Supplier<CrewScope> scope;
    private final ObjectMapper mapper;
    private final DiscussionMetrics metrics;
    private final java.util.function.LongSupplier clock;

    private final ResumeGates gates = new ResumeGates();
    private final CountDownLatch ready = new CountDownLatch(1);
    private final Map<String, Candidate> candidates = new ConcurrentHashMap<>();
    private final Map<Long, CompletableFuture<ThreadState>> rebuilding = new ConcurrentHashMap<>();
    private final Map<Long, ClosedThread> closed = Collections.synchronizedMap(new LinkedHashMap<>() {
        @Override
        protected boolean removeEldestEntry(Map.Entry<Long, ClosedThread> eldest) {
            return size() > MAX_CLOSED_REMEMBERED;
        }
    });

    CoordinatorResume(boolean enabled, DiscussionOrchestrator orchestrator, BusReader reader,
                      Supplier<CrewScope> scope, ObjectMapper mapper, DiscussionMetrics metrics,
                      java.util.function.LongSupplier clock) {
        this.enabled = enabled;
        this.orchestrator = orchestrator;
        this.reader = reader;
        this.scope = scope;
        this.mapper = mapper;
        this.metrics = metrics;
        this.clock = clock;
    }

    /** Resume switched off: every method returns at once and nothing is held back. */
    static CoordinatorResume off() {
        return new CoordinatorResume(false, null, null, null, null, null, System::currentTimeMillis);
    }

    /** The resume for this runtime: on only when {@link #CONFIG_KEY} is true and the coordinator log is on. */
    static CoordinatorResume fromConfig(DiscussionOrchestrator orchestrator, NatsConnectionProvider natsProvider,
                                        ObjectMapper mapper, DiscussionMetrics metrics) {
        boolean on = ConfigProvider.getConfig().getOptionalValue(CONFIG_KEY, Boolean.class).orElse(false);
        if (!on) {
            return off();
        }
        if (!orchestrator.recorder().enabled()) {
            log.warn("KUBEMOOT_COORDINATOR_RESUME needs KUBEMOOT_COORDINATOR_EVENT_LOG: a thread is resumed from its "
                    + "log, so resume stays off");
            return off();
        }
        log.info("Coordinator resume is on: threads left in flight are picked up from {}", CrewScope.COORDINATOR_STREAM);
        return new CoordinatorResume(true, orchestrator, new NatsBusReader(natsProvider), natsProvider::scope, mapper,
                metrics, System::currentTimeMillis);
    }

    boolean enabled() {
        return enabled;
    }

    // --- at start ---

    /**
     * Reads the crew's coordinator log and sorts its threads. Runs before the live subscription
     * starts, so a gate is shut for each thread still open before any live message can reach it.
     * A log that cannot be read leaves the coordinator as it would be without resume.
     */
    void discover() {
        if (!enabled) {
            return;
        }
        try {
            List<Candidate> found = group(reader.readAll(CrewScope.COORDINATOR_STREAM, scope.get().coordinatorWildcard()));
            found.forEach(this::sort);
            log.info("Coordinator log read: {} thread(s) in flight, {} closed", candidates.size(), closed.size());
        } catch (IOException | RuntimeException e) {
            log.warn("The coordinator log could not be read; threads left in flight are not resumed: {}", e.getMessage());
        } finally {
            ready.countDown();
        }
    }

    /** Starts resuming each thread {@link #discover} found open, on {@code executor}. */
    void launch(Executor executor) {
        if (!enabled) {
            return;
        }
        for (Candidate candidate : List.copyOf(candidates.values())) {
            try {
                executor.execute(() -> resumeThread(candidate));
            } catch (RejectedExecutionException e) {
                log.warn("Thread {} could not be scheduled for resume: {}", candidate.state().threadId, e.getMessage());
                finish(candidate.state(), null);
            }
        }
    }

    private List<Candidate> group(List<BusMessage> messages) {
        Map<String, List<CoordinatorEvent>> events = new LinkedHashMap<>();
        Map<String, Long> lastSequence = new HashMap<>();
        for (BusMessage message : messages) {
            CoordinatorEvent event = CoordinatorEvent.parse(mapper, message.data());
            if (event != null) {
                events.computeIfAbsent(event.threadId(), id -> new ArrayList<>()).add(event);
                lastSequence.put(event.threadId(), message.sequence());
            }
        }
        var out = new ArrayList<Candidate>();
        events.forEach((id, list) -> out.add(new Candidate(CoordinatorStateReducer.reduce(list), list, lastSequence.get(id))));
        return out;
    }

    /** A closed thread is remembered by its request; an open one becomes a candidate behind a shut gate. */
    private void sort(Candidate candidate) {
        CoordinatorThreadState state = candidate.state();
        if (state.openedAt == null) {
            log.warn("Thread {} has events but no thread_opened; it is not resumed", state.threadId);
            return;
        }
        if (state.closed()) {
            rememberClosed(state);
            return;
        }
        candidates.put(state.threadId, candidate);
        gates.shut(state.threadId);
        if (state.requestSeq > 0) {
            rebuilding.put(state.requestSeq, new CompletableFuture<>());
        }
    }

    private void rememberClosed(CoordinatorThreadState state) {
        if (state.requestSeq > 0 && !CLOSE_UNLOGGED.equals(state.closeReason)) {
            closed.put(state.requestSeq, new ClosedThread(state.threadId, state.answer));
        }
    }

    // --- live messages and requests ---

    /** True when the message was queued behind the shut gate of a thread being resumed. */
    boolean holdsLive(String threadId, String data) {
        return enabled && gates.hold(threadId, data);
    }

    /**
     * The thread the request at {@code requestSeq} already started, or null when it started
     * none (or resume is off). Waits for the start-up read and for the thread's rebuild, so a
     * request handed again right after a restart finds its thread, not a second one.
     */
    RequestMatch matchRequest(long requestSeq) {
        if (!enabled || requestSeq <= 0) {
            return null;
        }
        awaitReady();
        awaitRebuilt(requestSeq);
        ThreadState live = orchestrator.threadForRequest(requestSeq);
        if (live != null) {
            return live.phase == Phase.CLOSED ? RequestMatch.closedWith(live.threadId, live.answer)
                    : RequestMatch.inFlight(live);
        }
        ClosedThread done = closed.get(requestSeq);
        return done == null ? null : RequestMatch.closedWith(done.threadId(), done.answer());
    }

    private void awaitReady() {
        try {
            if (!ready.await(READY_WAIT_SECONDS, TimeUnit.SECONDS)) {
                log.warn("The coordinator log was not read within {}s; treating the request as new", READY_WAIT_SECONDS);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private void awaitRebuilt(long requestSeq) {
        CompletableFuture<ThreadState> pending = rebuilding.get(requestSeq);
        if (pending == null) {
            return;
        }
        try {
            pending.get(REBUILD_WAIT_SECONDS, TimeUnit.SECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        } catch (Exception e) {
            log.warn("Thread of request {} was not rebuilt in time: {}", requestSeq, e.getMessage());
        }
    }

    // --- one thread ---

    private void resumeThread(Candidate candidate) {
        CoordinatorThreadState logged = candidate.state();
        ThreadState state = null;
        try {
            state = takeOver(candidate);
        } catch (IOException | RuntimeException e) {
            log.warn("Thread {} could not be resumed: {}", logged.threadId, e.toString());
            metrics.threadResumed("failed");
            closeUnresumable(candidate);
        } finally {
            finish(logged, state);
        }
    }

    /** Takes the thread over; returns it, or null when it was closed unresumed or another coordinator has it. */
    private ThreadState takeOver(Candidate candidate) throws IOException {
        CoordinatorThreadState logged = candidate.state();
        List<JsonNode> messages = readMessages(logged.threadId);
        if (leftUnlogged(messages)) {
            abandon(candidate);
            return null;
        }
        ThreadState state = ThreadRebuilder.build(logged);
        CoordinatorEventRecorder recorder = orchestrator.recorder();
        recorder.adopt(state, candidate.events(), candidate.lastSequence());
        if (!recorder.resumed(state, candidate.lastSequence())) {
            log.warn("Thread {} is written by another coordinator; not taking it over", logged.threadId);
            metrics.threadResumed("fenced");
            return null;
        }
        // The thread joins the map at once, so the helpers that look a thread up by id find it, but the
        // phase sweep leaves it alone (it holds the thread's transition guard) until the takeover is done.
        state.transitioning.set(true);
        orchestrator.registerResumed(state);
        try {
            orchestrator.rearmDeadlines(state, logged.pending, logged.lastEventAt.toEpochMilli(), clock.getAsLong());
            applyMissed(state, logged, messages);
            carryOn(state, logged, candidate.lastSequence());
        } finally {
            state.transitioning.set(false);
        }
        return state;
    }

    /**
     * Carries out what the last logged decision left undone. A message applied from the stream can
     * already have moved the thread (a stop sent while no coordinator ran starts the synthesis);
     * the table is for the phase the log ended in, so a thread that moved is left to what moved it.
     */
    private void carryOn(ThreadState state, CoordinatorThreadState logged, long lastSequence) {
        String action = state.phase.name().equals(logged.phase) ? ResumeContinuation.run(orchestrator, state, logged)
                : ResumeContinuation.MOVED_BY_REPLAY;
        metrics.threadResumed(action);
        log.info("Thread {} resumed in phase {} (last logged event {}): {}", state.threadId, logged.phase,
                lastSequence, action);
    }

    private List<JsonNode> readMessages(String threadId) throws IOException {
        var out = new ArrayList<JsonNode>();
        for (BusMessage message : reader.readAll(DISCUSS_STREAM, scope.get().threadWildcard(threadId))) {
            try {
                out.add(mapper.readTree(message.data()));
            } catch (IOException e) {
                log.debug("Skipping an unreadable message of thread {}: {}", threadId, e.getMessage());
            }
        }
        return out;
    }

    /**
     * True when the thread's log is incomplete: the coordinator published one of its own
     * messages with no logged decision behind it. With the log working, every such message
     * carries an id made from its event; one with a random id was published after an append
     * failed, so the log ends before the thread did and cannot be resumed from.
     */
    private boolean leftUnlogged(List<JsonNode> messages) {
        String coordinator = orchestrator.coordinatorName();
        return messages.stream().anyMatch(msg -> coordinator.equals(JsonFields.text(msg, "agentName"))
                && COORDINATOR_MESSAGES.contains(JsonFields.text(msg, "messageType"))
                && !CoordinatorEventRecorder.isDerivedId(JsonFields.text(msg, CoordinatorWire.MESSAGE_ID)));
    }

    /** Applies the thread's messages the log has not seen, in the order the stream holds them. */
    private void applyMissed(ThreadState state, CoordinatorThreadState logged, List<JsonNode> messages) {
        var filter = new ReplayFilter(logged.lastEventAt, logged.appliedMessageIds);
        for (JsonNode message : messages) {
            try {
                ThreadRebuilder.fillContent(state, logged, message);
                orchestrator.applyMessage(message, filter, state);
            } catch (RuntimeException e) {
                log.debug("A message of thread {} could not be applied: {}", state.threadId, e.getMessage());
            }
        }
    }

    /**
     * Closes a thread whose log is incomplete. Its agents get {@code thread_close} so they stop
     * working on it; the close is logged so the next start does not find it open again. The
     * request that started it is no longer tied to it: handed again, it starts a new thread.
     */
    private void abandon(Candidate candidate) {
        CoordinatorThreadState logged = candidate.state();
        ThreadState state = ThreadRebuilder.build(logged);
        state.requestSeq = 0;
        CoordinatorEventRecorder recorder = orchestrator.recorder();
        recorder.adopt(state, candidate.events(), candidate.lastSequence());
        orchestrator.registerResumed(state);
        String channel = DiscussionOrchestrator.resolveChannel(state);
        orchestrator.publishThreadClose(state.threadId, channel);
        state.phase = Phase.CLOSED;
        recorder.closed(state, CLOSE_UNLOGGED, channel);
        metrics.threadResumed("abandoned");
        log.warn("Thread {} was left with an incomplete log (an event could not be written); it is closed, not resumed",
                state.threadId);
    }

    /** A thread that could not be taken over is closed, so its agents stop and no live message makes a blank thread of it. */
    private void closeUnresumable(Candidate candidate) {
        try {
            abandon(candidate);
        } catch (RuntimeException e) {
            log.warn("Thread {} could not be closed either: {}", candidate.state().threadId, e.toString());
        }
    }

    /** Hands the rebuilt thread (null when there is none) to the request waiting for it, and opens the gate. */
    private void finish(CoordinatorThreadState logged, ThreadState state) {
        CompletableFuture<ThreadState> waiting = rebuilding.get(logged.requestSeq);
        if (waiting != null) {
            waiting.complete(state);
        }
        gates.open(logged.threadId, orchestrator::applyHeld);
        candidates.remove(logged.threadId);
    }
}
