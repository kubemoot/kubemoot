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
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CancellationException;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executor;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.function.LongSupplier;
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
 * <p>Without leases a single coordinator replica is assumed: the log's fenced appends keep a
 * second writer from acting on a thread, but nothing decides which replica owns it. With leases
 * ({@link CoordinatorLeases}) a replica takes over only a thread whose lease it wins, and it looks
 * again every {@link #SCAN_EVERY} ({@link #scan}), so a thread whose owner stopped is taken over
 * within the lease and one scan. A request handed again finds its thread in the log, whichever
 * replica owns it ({@link #matchRequest}).
 */
final class CoordinatorResume {

    /** The setting that turns resume on: environment variable {@code KUBEMOOT_COORDINATOR_RESUME}. */
    static final String CONFIG_KEY = "kubemoot.coordinator.resume";
    static final String DISCUSS_STREAM = "KUBEMOOT_DISCUSS";

    private static final Logger log = LoggerFactory.getLogger(CoordinatorResume.class);
    /** How long a request waits for the start-up read of the log before it is treated as new. */
    private static final long READY_WAIT_SECONDS = 60;
    /** With leases: how often a replica looks in the log for open threads nobody holds a lease for. */
    static final Duration SCAN_EVERY = Duration.ofSeconds(10);
    /** With leases: the least the scan and the lookup of a request read back in the coordinator log. */
    private static final Duration MIN_LOG_WINDOW = Duration.ofHours(1);
    /** With leases: how long a request waits between looks at a thread another replica owns. */
    private static final long FOLLOW_POLL_MILLIS = 2_000;
    /** With leases: how long past the hard ceiling a request waits for another replica's thread before it gives up. */
    private static final long FOLLOW_GRACE_SECONDS = 120;
    /** How long a request waits for the thread it names to be rebuilt. */
    private static final long REBUILD_WAIT_SECONDS = 120;
    /** Closed threads remembered by request, so a request handed again after its close is answered, not rerun. */
    private static final int MAX_CLOSED_REMEMBERED = 500;
    private static final String CLOSE_UNLOGGED = "unlogged";
    private static final String CLOSE_EXPIRED = "expired";
    /** The setting for the resume age limit: environment variable {@code KUBEMOOT_COORDINATOR_RESUME_MAX_AGE}. */
    static final String MAX_AGE_KEY = "kubemoot.coordinator.resume-max-age";
    /** The messages the coordinator publishes about a thread; with the log on each carries an id made from its event. */
    private static final Set<String> COORDINATOR_MESSAGES = Set.of(DiscussionOrchestrator.MSG_THREAD_START,
            DiscussionOrchestrator.MSG_ADVISORY, SubcommitteeLine.MESSAGE_TYPE, DiscussionOrchestrator.MSG_ADVISORY_READY,
            DiscussionOrchestrator.MSG_REVIEW_READY, DiscussionOrchestrator.MSG_SYNTHESIS,
            DiscussionOrchestrator.MSG_THREAD_CLOSE);

    private record Candidate(CoordinatorThreadState state, List<CoordinatorEvent> events, long lastSequence) {
    }

    /** A message read back from the discussion stream, with its sequence there. */
    private record Streamed(long sequence, JsonNode node) {
    }

    private record ClosedThread(String threadId, String answer) {
    }

    private final boolean enabled;
    private final DiscussionOrchestrator orchestrator;
    private final BusReader reader;
    private final Supplier<CrewScope> scope;
    private final ObjectMapper mapper;
    private final DiscussionMetrics metrics;
    private final LongSupplier clock;

    private final ResumeGates gates = new ResumeGates();
    /** Threads whose takeover has started, so a scan that meets a candidate again does not start it twice. */
    private final Set<String> launched = ConcurrentHashMap.newKeySet();
    private final CountDownLatch ready = new CountDownLatch(1);
    private final Map<String, Candidate> candidates = new ConcurrentHashMap<>();
    private final Map<Long, CompletableFuture<ThreadState>> rebuilding = new ConcurrentHashMap<>();
    private final Map<Long, ClosedThread> closed = Collections.synchronizedMap(new LinkedHashMap<>() {
        @Override
        protected boolean removeEldestEntry(Map.Entry<Long, ClosedThread> eldest) {
            return size() > MAX_CLOSED_REMEMBERED;
        }
    });

    private final Duration maxAge;
    private final CoordinatorLeases leases;

    /** A resume with no age limit. */
    CoordinatorResume(boolean enabled, DiscussionOrchestrator orchestrator, BusReader reader,
                      Supplier<CrewScope> scope, ObjectMapper mapper, DiscussionMetrics metrics,
                      LongSupplier clock) {
        this(enabled, orchestrator, reader, scope, mapper, metrics, clock, Duration.ZERO);
    }

    /** @param maxAge how old a thread's last event may be for it to be resumed; zero for no limit */
    CoordinatorResume(boolean enabled, DiscussionOrchestrator orchestrator, BusReader reader,
                      Supplier<CrewScope> scope, ObjectMapper mapper, DiscussionMetrics metrics,
                      LongSupplier clock, Duration maxAge) {
        this(enabled, orchestrator, reader, scope, mapper, metrics, clock, maxAge, CoordinatorLeases.off());
    }

    /** @param leases who may take a thread over; {@link CoordinatorLeases#off()} for a single replica */
    CoordinatorResume(boolean enabled, DiscussionOrchestrator orchestrator, BusReader reader,
                      Supplier<CrewScope> scope, ObjectMapper mapper, DiscussionMetrics metrics,
                      LongSupplier clock, Duration maxAge, CoordinatorLeases leases) {
        this.leases = leases;
        this.maxAge = maxAge;
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
            if (CoordinatorLeases.configured()) {
                log.warn("KUBEMOOT_COORDINATOR_LEASE needs KUBEMOOT_COORDINATOR_RESUME: a replica takes a thread over "
                        + "through resume, so leases stay off");
            }
            return off();
        }
        if (!orchestrator.recorder().enabled()) {
            log.warn("KUBEMOOT_COORDINATOR_RESUME needs KUBEMOOT_COORDINATOR_EVENT_LOG: a thread is resumed from its "
                    + "log, so resume stays off");
            return off();
        }
        Duration maxAge = configuredMaxAge();
        log.info("Coordinator resume is on: threads left in flight are picked up from {}; age limit {}",
                CrewScope.COORDINATOR_STREAM, maxAge.isZero() ? "none" : maxAge);
        CoordinatorLeases leases = CoordinatorLeases.configured()
                ? CoordinatorLeases.forRuntime(natsProvider, orchestrator::threadIsOpen, orchestrator::threadLost)
                : CoordinatorLeases.off();
        return new CoordinatorResume(true, orchestrator, new NatsBusReader(natsProvider), natsProvider::scope, mapper,
                metrics, System::currentTimeMillis, maxAge, leases);
    }

    /** The age limit from {@link #MAX_AGE_KEY}: the default when unset, the default with a warning when not a duration. */
    private static Duration configuredMaxAge() {
        return ResumeAge.from(ConfigProvider.getConfig().getOptionalValue(MAX_AGE_KEY, String.class),
                text -> log.warn("KUBEMOOT_COORDINATOR_RESUME_MAX_AGE={} is not a duration such as 30m; using {}", text,
                        ResumeAge.DEFAULT));
    }

    boolean enabled() {
        return enabled;
    }

    /** The leases this resume takes threads with; off for a single replica. */
    CoordinatorLeases leases() {
        return leases;
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
            if (!launched.add(candidate.state().threadId)) {
                continue;
            }
            try {
                executor.execute(() -> resumeThread(candidate));
            } catch (RejectedExecutionException e) {
                log.warn("Thread {} could not be scheduled for resume: {}", candidate.state().threadId, e.getMessage());
                finish(candidate.state(), null);
            }
        }
    }

    /**
     * With leases: looks in the log for open threads no replica holds a lease for, wins the lease of
     * each, and resumes it on {@code executor}. A thread whose owner died or was cut off is taken
     * over within the lease and one scan. Does nothing without leases.
     */
    void scan(Executor executor) {
        if (!enabled || !leases.enabled()) {
            return;
        }
        try {
            group(reader.readSince(CrewScope.COORDINATOR_STREAM, scope.get().coordinatorWildcard(), logWindowStart()))
                    .forEach(this::sort);
            launch(executor);
        } catch (IOException | RuntimeException e) {
            log.warn("The coordinator log could not be scanned for threads to take over: {}", e.getMessage());
        }
    }

    private Instant logWindowStart() {
        Duration window = maxAge.compareTo(MIN_LOG_WINDOW) > 0 ? maxAge : MIN_LOG_WINDOW;
        return Instant.ofEpochMilli(clock.getAsLong()).minus(window);
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
        if (!claim(state.threadId)) {
            return;
        }
        candidates.put(state.threadId, candidate);
        gates.shut(state.threadId);
        if (state.requestSeq > 0) {
            rebuilding.put(state.requestSeq, new CompletableFuture<>());
        }
    }

    /**
     * True when this replica may take the open thread over: it is not already here or being taken,
     * and, with leases, no other replica holds its lease. The lease is won before the gate is shut,
     * and the thread's messages are read after, so none that arrives in between is missed.
     */
    private boolean claim(String threadId) {
        if (candidates.containsKey(threadId) || orchestrator.holdsThread(threadId)) {
            return false;
        }
        return !leases.enabled() || leases.acquire(threadId);
    }

    private void rememberClosed(CoordinatorThreadState state) {
        boolean closedUnresumed = CLOSE_UNLOGGED.equals(state.closeReason) || CLOSE_EXPIRED.equals(state.closeReason);
        if (state.requestSeq > 0 && !closedUnresumed) {
            closed.put(state.requestSeq, new ClosedThread(state.threadId, state.answer));
        }
    }

    // --- live messages and requests ---

    /** True while the thread is a candidate for takeover or its takeover is running. */
    boolean isResuming(String threadId) {
        return candidates.containsKey(threadId) || launched.contains(threadId);
    }

    /** True when the message was queued behind the shut gate of a thread being resumed. */
    boolean holdsLive(String threadId, String data) {
        return holdsLive(threadId, data, 0);
    }

    /** {@link #holdsLive(String, String)} for a message at {@code streamSequence} of the discussion stream. */
    boolean holdsLive(String threadId, String data, long streamSequence) {
        return enabled && gates.hold(threadId, data, streamSequence);
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
        RequestMatch known = knownMatch(requestSeq);
        return known != null || !leases.enabled() ? known : followInLog(requestSeq);
    }

    /** The thread of the request that this replica holds, or remembers having closed; null when it knows none. */
    private RequestMatch knownMatch(long requestSeq) {
        ThreadState live = orchestrator.threadForRequest(requestSeq);
        if (live != null) {
            return live.phase == Phase.CLOSED ? RequestMatch.closedWith(live.threadId, live.answer)
                    : RequestMatch.inFlight(live);
        }
        ClosedThread done = closed.get(requestSeq);
        return done == null ? null : RequestMatch.closedWith(done.threadId(), done.answer());
    }

    /**
     * With leases: the thread the request started may be owned by another replica, so the log is
     * asked. A closed thread answers from its logged answer. An open thread nobody holds is taken
     * over here. An open thread another replica holds is waited for, looking again every
     * {@link #FOLLOW_POLL_MILLIS}, until it closes or its lease lapses. No thread in the log means
     * the request is new. A wait that outlasts the hard ceiling plus a margin fails the request, so
     * it is handed again; it never starts a second thread for a request that has one.
     */
    private RequestMatch followInLog(long requestSeq) {
        long giveUpAt = clock.getAsLong() + (orchestrator.hardCeilingSeconds() + FOLLOW_GRACE_SECONDS) * 1000;
        while (clock.getAsLong() < giveUpAt) {
            Candidate found = findByRequest(requestSeq);
            if (found == null) {
                return null;
            }
            if (found.state().closed()) {
                return closedMatch(found.state());
            }
            RequestMatch mine = takeOrFind(found, requestSeq);
            if (mine != null) {
                return mine;
            }
            pause();
        }
        throw new IllegalStateException("request " + requestSeq + " is owned by another coordinator past the hard ceiling");
    }

    /** The thread of the request when it is here, taking it over first when no replica holds its lease; else null. */
    private RequestMatch takeOrFind(Candidate found, long requestSeq) {
        RequestMatch here = knownMatch(requestSeq);
        if (here != null) {
            return here;
        }
        sort(found);
        String threadId = found.state().threadId;
        if (!candidates.containsKey(threadId)) {
            return null;
        }
        if (launched.add(threadId)) {
            resumeThread(found);
        } else {
            awaitRebuilt(requestSeq);
        }
        return knownMatch(requestSeq);
    }

    /** A closed thread answers its request, unless it was closed without an answer being resumed (the request then starts anew). */
    private static RequestMatch closedMatch(CoordinatorThreadState state) {
        boolean unresumed = CLOSE_UNLOGGED.equals(state.closeReason) || CLOSE_EXPIRED.equals(state.closeReason);
        return unresumed ? null : RequestMatch.closedWith(state.threadId, state.answer);
    }

    /** The newest thread in the log that the request started; null when there is none. */
    private Candidate findByRequest(long requestSeq) {
        try {
            Candidate newest = null;
            for (Candidate candidate : group(reader.readSince(CrewScope.COORDINATOR_STREAM,
                    scope.get().coordinatorWildcard(), logWindowStart()))) {
                CoordinatorThreadState state = candidate.state();
                if (state.requestSeq == requestSeq && state.openedAt != null
                        && (newest == null || state.openedAt.isAfter(newest.state().openedAt))) {
                    newest = candidate;
                }
            }
            return newest;
        } catch (IOException | RuntimeException e) {
            throw new IllegalStateException("the coordinator log could not be read for request " + requestSeq, e);
        }
    }

    private void pause() {
        try {
            Thread.sleep(FOLLOW_POLL_MILLIS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new CancellationException("interrupted while waiting for the thread of a request");
        }
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
        if (tooOld(logged)) {
            expire(candidate);
            return null;
        }
        List<Streamed> messages = readMessages(logged.threadId);
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

    private List<Streamed> readMessages(String threadId) throws IOException {
        var out = new ArrayList<Streamed>();
        for (BusMessage message : reader.readAll(DISCUSS_STREAM, scope.get().threadWildcard(threadId))) {
            try {
                out.add(new Streamed(message.sequence(), mapper.readTree(message.data())));
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
    private boolean leftUnlogged(List<Streamed> messages) {
        String coordinator = orchestrator.coordinatorName();
        return messages.stream().map(Streamed::node).anyMatch(msg -> coordinator.equals(JsonFields.text(msg, "agentName"))
                && COORDINATOR_MESSAGES.contains(JsonFields.text(msg, "messageType"))
                && !CoordinatorEventRecorder.isDerivedId(JsonFields.text(msg, CoordinatorWire.MESSAGE_ID)));
    }

    /** Applies the thread's messages the log has not seen, in the order the stream holds them. */
    private void applyMissed(ThreadState state, CoordinatorThreadState logged, List<Streamed> messages) {
        var filter = new ReplayFilter(logged.lastEventAt, logged.appliedMessageIds, logged.discussSeq);
        for (Streamed message : messages) {
            try {
                ThreadRebuilder.fillContent(state, logged, message.node());
                orchestrator.applyMessage(message.node(), filter, state, message.sequence());
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
        closeUnresumed(candidate, CLOSE_UNLOGGED, "abandoned");
        log.warn("Thread {} was left with an incomplete log (an event could not be written); it is closed, not resumed",
                candidate.state().threadId);
    }

    /**
     * Closes a thread whose last event is older than the resume age limit: the asker has long
     * stopped waiting, and resuming it would spend accelerator time for nobody. Closed the same
     * way as an incomplete one, with the reason {@code expired}.
     */
    private void expire(Candidate candidate) {
        closeUnresumed(candidate, CLOSE_EXPIRED, "expired");
        log.warn("Thread {} last logged an event at {}, more than {} ago; it is closed, not resumed",
                candidate.state().threadId, candidate.state().lastEventAt, maxAge);
    }

    /**
     * Closes a thread this coordinator will not resume: its agents get {@code thread_close}, the
     * close is logged with {@code reason}, and the request that started it is no longer tied to it.
     */
    private void closeUnresumed(Candidate candidate, String reason, String action) {
        ThreadState state = ThreadRebuilder.build(candidate.state());
        state.requestSeq = 0;
        CoordinatorEventRecorder recorder = orchestrator.recorder();
        recorder.adopt(state, candidate.events(), candidate.lastSequence());
        orchestrator.registerResumed(state);
        String channel = DiscussionOrchestrator.resolveChannel(state);
        orchestrator.publishThreadClose(state.threadId, channel);
        state.phase = Phase.CLOSED;
        recorder.closed(state, reason, channel);
        metrics.threadResumed(action);
    }

    /** True when the thread's last logged event is older than the resume age limit (none when the limit is off). */
    private boolean tooOld(CoordinatorThreadState logged) {
        return !maxAge.isZero()
                && Duration.between(logged.lastEventAt, Instant.ofEpochMilli(clock.getAsLong())).compareTo(maxAge) > 0;
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
        gates.openHeld(logged.threadId, held -> orchestrator.applyHeld(held.data(), held.streamSequence()));
        candidates.remove(logged.threadId);
        launched.remove(logged.threadId);
        if (state == null) {
            leases.release(logged.threadId);
        }
    }
}
