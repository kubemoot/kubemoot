package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.CoordinatorEventLog.AppendResult;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.MissingNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.eclipse.microprofile.config.ConfigProvider;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Instant;
import java.util.Collection;
import java.util.List;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.function.BiFunction;
import java.util.function.BooleanSupplier;
import java.util.function.Supplier;

import static ai.kubemoot.agent.nats.CoordinatorEvent.*;
import static ai.kubemoot.agent.nats.CoordinatorWire.*;

/**
 * Writes the coordinator's decisions to the thread's log. The orchestrator calls one
 * method here after each decision, passing the thread; the recorder reads the thread's
 * state after the decision and writes what the decision changed, so the orchestrator
 * holds no event-building code. With the event log off every method returns at once.
 *
 * <p>Each method returns whether the coordinator may carry out what the decision
 * leads to. While the log is up that is the write-ahead rule: a decision's effect (a
 * brief, a phase signal, the answer) is published only after its event is appended.
 * When an append fails the log is down for that thread: the effect is published anyway,
 * the thread is marked unlogged (not resumable, since its log is incomplete), and no
 * further event is appended to it. The only false is a fence.
 *
 * <p>Every append states the last sequence this writer saw on the thread's subject. When
 * the log refuses it because another writer appended in between, the thread is fenced:
 * this coordinator no longer owns it, every later method returns false for it, and the
 * orchestrator stops acting on it ({@link #fenced}).
 *
 * <p>A bug while building an event is treated as a failed append and never stops a discussion.
 * The events of one thread are built and appended one at a time, in order.
 */
final class CoordinatorEventRecorder {

    /** The setting that turns the log on: environment variable {@code KUBEMOOT_COORDINATOR_EVENT_LOG}. */
    static final String CONFIG_KEY = "kubemoot.coordinator.event-log";

    private static final Logger log = LoggerFactory.getLogger(CoordinatorEventRecorder.class);
    private static final String MESSAGE_SUFFIX = "/msg/";
    private static final String MSG_THREAD_CLOSE = "thread_close";

    private final boolean enabled;
    private final CoordinatorEventLog eventLog;
    private final BiFunction<String, String, String> subjectFor;
    private final String coordinator;
    private final ObjectMapper mapper;
    private final Supplier<Instant> clock;
    private final DiscussionMetrics metrics;

    CoordinatorEventRecorder(boolean enabled, CoordinatorEventLog eventLog,
                             BiFunction<String, String, String> subjectFor, String coordinator,
                             ObjectMapper mapper, Supplier<Instant> clock, DiscussionMetrics metrics) {
        this.enabled = enabled;
        this.eventLog = eventLog;
        this.subjectFor = subjectFor;
        this.coordinator = coordinator;
        this.mapper = mapper;
        this.clock = clock;
        this.metrics = metrics;
    }

    /** A recorder that writes nothing. */
    static CoordinatorEventRecorder off() {
        return new CoordinatorEventRecorder(false, CoordinatorEventLog.OFF, (crew, id) -> id, "", new ObjectMapper(),
                Instant::now, null);
    }

    /** The recorder for this runtime: on only when {@link #CONFIG_KEY} is true. */
    static CoordinatorEventRecorder fromConfig(String coordinator, NatsConnectionProvider natsProvider,
                                               ObjectMapper mapper, DiscussionMetrics metrics) {
        boolean on = ConfigProvider.getConfig().getOptionalValue(CONFIG_KEY, Boolean.class).orElse(false);
        if (!on) {
            return off();
        }
        log.info("Coordinator event log is on: decisions are written to {}", CrewScope.COORDINATOR_STREAM);
        return new CoordinatorEventRecorder(true, new NatsCoordinatorEventLog(natsProvider, mapper),
                (crew, threadId) -> natsProvider.scope().forCrew(crew).coordinatorSubject(threadId),
                coordinator, mapper, Instant::now, metrics);
    }

    boolean enabled() {
        return enabled;
    }

    /** True once another coordinator's append showed this one no longer owns the thread. */
    boolean fenced(ThreadState s) {
        return s != null && s.trace.fenced();
    }

    /** True once an append failed for the thread: its log is incomplete and it is not resumable. */
    boolean unlogged(ThreadState s) {
        return s != null && s.trace.unlogged();
    }

    /**
     * The id of the bus message {@code messageType} that carries out the decision last
     * written as an event of {@code eventType}, or null when the log is not recording the
     * thread (the message then gets a random id). The same decision made twice has the
     * same id, so a repeat is dropped by the stream and by every consumer.
     */
    String messageId(ThreadState s, String eventType, String messageType) {
        String eventId = enabled && s != null ? s.trace.lastEventId(eventType) : null;
        boolean thisRound = eventId != null && !s.trace.unlogged()
                && eventId.startsWith(s.threadId + "/" + s.trace.round() + "/");
        return thisRound ? eventId + MESSAGE_SUFFIX + messageType : null;
    }

    /** The id of the {@code thread_close} message: derived from the id of the thread's close event. */
    String closeMessageId(ThreadState s) {
        boolean logged = enabled && s != null && s.trace.opened() && !s.trace.unlogged();
        return logged ? keyed(s, "closed") + MESSAGE_SUFFIX + MSG_THREAD_CLOSE : null;
    }

    // --- decisions, in the order a thread meets them ---

    /** The thread exists: started by a request this coordinator took, or by an external {@code thread_start}. */
    boolean opened(ThreadState s, String source) {
        if (!enabled) {
            return true;
        }
        s.trace.markOpened();
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            putText(d, "crew", s.crew);
            putText(d, "conversationId", s.conversationId);
            putText(d, "userQuery", s.userQuery);
            putText(d, "primaryChannel", s.primaryChannel);
            d.put("source", source);
            d.put("openedAt", s.threadCreated.toString());
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            return emit(s, THREAD_OPENED, s.threadId + "/opened", d);
        });
    }

    /** The thread moved to its current phase (anything but closed, which has its own event). */
    boolean phaseEntered(ThreadState s, String reason) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE, s.phase.name());
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            putText(d, "phaseBeforePause", s.phaseBeforePause == null ? null : s.phaseBeforePause.name());
            d.put(ADVISORY_PENDING, s.advisoryPending.get());
            d.put(REASON, reason);
            return emit(s, PHASE_ENTERED, keyed(s, "phase/" + s.phase.name() + "/" + s.trace.enter(s.phase.name())), d);
        });
    }

    /** The advisory step chose who to convene, by reasoning, by a fallback, or not at all (everyone may take part). */
    boolean subcommitteeConvened(ThreadState s, DiscussionOrchestrator.ReasoningResult reasoning,
                                 DiscussionOrchestrator.AdvisoryOutcome outcome) {
        return write(s, () -> {
            boolean byReasoning = reasoning != null && !reasoning.selected().isEmpty();
            return subcommittee(s, byReasoning ? "reasoning" : fallbackPath(s), outcome.wisdom(), outcome.technologies());
        });
    }

    /** The advisory step failed: no subcommittee was chosen and every agent may take part. */
    boolean advisoryFailed(ThreadState s) {
        return write(s, () -> subcommittee(s, "broadcast", null, List.of()));
    }

    /** The coordinator chose no toolers and answered from its own reasoning. */
    boolean answeredDirectly(ThreadState s, DiscussionOrchestrator.ReasoningResult reasoning) {
        return write(s, () -> subcommittee(s, "direct", reasoning.brief(), reasoning.technologies()));
    }

    /** The advisory step ended and the evaluation clock reset: signals cleared, roster set. */
    boolean evaluationStarted(ThreadState s) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.set(ROSTER, strings(s.phaseRoster.get().members()));
            d.put("startedAt", s.phaseStarted.toString());
            return emit(s, EVALUATION_STARTED, keyed(s, "evaluation/" + s.trace.enter(EVALUATION_STARTED)), d);
        });
    }

    /** Review began: the analysts it woke, each pending with a deadline. */
    boolean reviewStarted(ThreadState s) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.set(ROSTER, strings(s.phaseRoster.get().members()));
            ObjectNode pending = d.putObject(PENDING);
            new TreeMap<>(s.pendingEvaluations).forEach(pending::put);
            return emit(s, REVIEW_STARTED, keyed(s, "review/" + s.trace.enter(REVIEW_STARTED)), d);
        });
    }

    /**
     * An agent's signal was applied to the thread. Records the signals that change state
     * (not a heartbeat, which only refreshes a deadline); a failure is its own event.
     */
    boolean signalApplied(ThreadState s, String agent, String messageType, JsonNode message) {
        return write(s, () -> {
            JsonNode msg = message == null ? MissingNode.getInstance() : message;
            if (FAILURE.equals(messageType)) {
                return failure(s, agent, msg, false);
            }
            String signal = signalFor(messageType);
            return signal == null || signal(s, agent, signal, msg);
        });
    }

    /** A waking or ready signal from an agent starting up put it on the thread's pending list. */
    boolean wake(ThreadState s, String agent, String signal, long deadlineAt) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(AGENT, agent);
            d.put(SIGNAL, signal);
            d.put(DEADLINE_AT, deadlineAt);
            return emit(s, SIGNAL_APPLIED, keyed(s, "signal/" + agent + "/" + signal + "/" + deadlineAt), d);
        });
    }

    /**
     * An agent's deadline passed and the coordinator recorded a failure for it, as it did
     * for an agent that sent none.
     */
    boolean timedOut(ThreadState s, String agent) {
        return write(s, () -> failure(s, agent, MissingNode.getInstance(), true));
    }

    /** A phase ended because its participants settled; {@code basis} says why. */
    boolean settled(ThreadState s, String basis) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE, s.phase.name());
            d.put("basis", basis);
            ObjectNode counts = d.putObject("counts");
            counts.put(AGREE, s.agreeSignals.size());
            counts.put(CONCERN, s.concernSignals.size());
            counts.put("standAside", s.standAsideSignals.size());
            counts.put(BLOCK, s.blockSignals.size());
            counts.put(FAILURE, s.failureSignals.size());
            return emit(s, SETTLED, keyed(s, "settled/" + s.phase.name() + "/" + s.trace.enter(SETTLED + s.phase.name())), d);
        });
    }

    /** The synthesis call is about to begin. */
    boolean synthesisStarted(ThreadState s) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put("agrees", s.agreeSignals.size());
            d.put("concerns", s.concernSignals.size());
            d.put("standAsides", s.standAsideSignals.size());
            return emit(s, SYNTHESIS_STARTED, keyed(s, "synthesis/started/" + s.trace.enter(SYNTHESIS_STARTED)), d);
        });
    }

    /**
     * The answer exists and is about to be published. {@code outcome} is how it came to
     * be: {@code synthesized}, {@code capacity}, {@code direct}, {@code fallback} or
     * {@code ceiling}. The answer is in the log before it is on the bus.
     */
    boolean synthesisDone(ThreadState s, String outcome, String channel, String response) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put("outcome", outcome);
            d.put(CHANNEL, channel);
            d.put("response", DiscussionOrchestrator.publishedContent("synthesis", response));
            ObjectNode tokens = d.putObject("tokens");
            tokens.put("in", s.synthesisInputTokens);
            tokens.put("out", s.synthesisOutputTokens);
            d.put("promptTooLarge", s.synthesisPromptTooLarge);
            return emit(s, SYNTHESIS_DONE, keyed(s, "synthesis/done/" + s.trace.enter(SYNTHESIS_DONE)), d);
        });
    }

    /** A human reply reopened a closed thread: a new round begins with no roster and no signals. */
    boolean reopened(ThreadState s) {
        return write(s, () -> {
            int round = s.trace.nextRound();
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            d.set(ROSTER, mapper.createArrayNode());
            return emit(s, THREAD_REOPENED, s.threadId + "/reopened/" + round, d);
        });
    }

    /** The thread is closed; {@code reason} says how (an outcome of {@link #synthesisDone}, {@code zombie} or {@code no_signals}). */
    boolean closed(ThreadState s, String reason, String channel) {
        return write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(REASON, reason);
            d.put(CHANNEL, channel);
            d.put(ADVISORY_PENDING, s.advisoryPending.get());
            return emit(s, THREAD_CLOSED, keyed(s, "closed"), d);
        });
    }

    // --- the check at close ---

    /**
     * Compares the closed thread's live state with the state the reducer rebuilds from the
     * events the log acknowledged. A mismatch means the log would not rebuild this thread:
     * it is logged as a warning with the differing fields and counted, and nothing else
     * happens, so a mismatch never fails the thread. Pending deadlines are not compared
     * (a heartbeat moves the live one without an event).
     */
    void checkAtClose(ThreadState s) {
        if (!enabled || !s.trace.opened() || s.trace.fenced()) {
            return;
        }
        if (s.trace.unlogged()) {
            metrics.logChecked("unlogged");
            return;
        }
        try {
            List<CoordinatorEvent> events = s.trace.kept();
            if (events == null) {
                log.debug("Thread {} wrote more than {} events; its log is not checked", s.threadId,
                        CoordinatorTrace.MAX_KEPT_EVENTS);
                return;
            }
            List<String> differences = CoordinatorStateReducer.reduce(events).view().differences(ThreadView.of(s));
            metrics.logChecked(differences.isEmpty() ? "match" : "mismatch");
            if (!differences.isEmpty()) {
                log.warn("Coordinator log of thread {} does not rebuild its live state: {}", s.threadId, differences);
            }
        } catch (RuntimeException e) {
            log.warn("The coordinator log of thread {} could not be checked: {}", s.threadId, e.toString());
        }
    }

    // --- building and writing ---

    /**
     * Runs {@code body}, which appends the event, when the log is on and the thread was
     * opened, one event at a time per thread. Returns whether the coordinator may carry
     * out the decision: false only for a fenced thread. An unlogged thread (an append
     * failed) gets no more events and every decision is allowed. A bug while building the
     * event makes the thread unlogged too: its log would be incomplete.
     */
    private boolean write(ThreadState s, BooleanSupplier body) {
        if (!enabled || !s.trace.opened() || s.trace.unlogged()) {
            return true;
        }
        if (s.trace.fenced()) {
            return false;
        }
        try {
            synchronized (s.trace.lock()) {
                // A writer queued on the lock behind one whose append failed or was refused must not append.
                if (s.trace.fenced()) {
                    return false;
                }
                return s.trace.unlogged() || body.getAsBoolean();
            }
        } catch (RuntimeException e) {
            log.debug("A coordinator event for thread {} could not be built", s.threadId, e);
            markUnlogged(s, "an event that could not be built (" + e + ")");
            return true;
        }
    }

    private boolean subcommittee(ThreadState s, String path, String brief, List<String> technologies) {
        ObjectNode d = mapper.createObjectNode();
        d.put("path", path);
        d.set("members", strings(s.innerCircle));
        putText(d, "brief", brief);
        d.set("technologies", strings(technologies));
        d.set("skills", strings(s.selectedSkills));
        d.put("confidence", s.triageConfidence);
        putText(d, "primaryChannel", s.primaryChannel);
        ObjectNode tokens = d.putObject("tokens");
        tokens.put("advisoryIn", s.advisoryInputTokens);
        tokens.put("advisoryOut", s.advisoryOutputTokens);
        tokens.put("triageIn", s.triageInputTokens);
        tokens.put("triageOut", s.triageOutputTokens);
        return emit(s, SUBCOMMITTEE_CONVENED, keyed(s, "subcommittee/" + s.trace.enter(SUBCOMMITTEE_CONVENED)), d);
    }

    /** A signal that changes state: the agent, its message, and the deadline and details that go with the kind. */
    private boolean signal(ThreadState s, String agent, String signal, JsonNode msg) {
        ObjectNode d = mapper.createObjectNode();
        d.put(AGENT, agent);
        d.put(SIGNAL, signal);
        String messageId = JsonFields.text(msg, MESSAGE_ID);
        d.put(MESSAGE_ID, messageId);
        Long deadline = s.pendingEvaluations.get(agent);
        if (deadline != null) {
            d.put(DEADLINE_AT, deadline);
        }
        signalDetails(d, s, agent, signal, msg);
        return emit(s, SIGNAL_APPLIED, keyed(s, "signal/" + agent + "/" + messageKey(s, agent, messageId, msg)), d);
    }

    /**
     * A failure: reported by the agent ({@code msg} is its message), or recorded by the
     * coordinator when the agent's deadline passed ({@code synthesized}, no message).
     */
    private boolean failure(ThreadState s, String agent, JsonNode msg, boolean synthesized) {
        ObjectNode d = mapper.createObjectNode();
        d.put(AGENT, agent);
        d.put(CAUSE, synthesized ? "evaluation_timeout" : "reported");
        d.put(SYNTHESIZED, synthesized);
        d.set(FAILED_SOURCES, strings(synthesized ? List.of() : DiscussionOrchestrator.failedSourceNames(msg)));
        String messageId = synthesized ? "" : JsonFields.text(msg, MESSAGE_ID);
        d.put(MESSAGE_ID, messageId);
        putTime(d, LAST_SIGNAL_AT, s.lastSignalReceived);
        String key = synthesized ? "timeout" : messageKey(s, agent, messageId, msg);
        return emit(s, FAILURE_RECORDED, keyed(s, "failure/" + agent + "/" + key), d);
    }

    /** Where a subcommittee chosen without the reasoning call came from: a fallback selection, or nobody. */
    private static String fallbackPath(ThreadState s) {
        Set<String> circle = s.innerCircle;
        return circle == null || circle.isEmpty() ? "broadcast" : "fallback";
    }

    /** The fields only some signals have: who is a researcher, why an agent could not answer, when it stopped being quiet. */
    private void signalDetails(ObjectNode d, ThreadState s, String agent, String signal, JsonNode msg) {
        if (AGREE.equals(signal)) {
            d.put(RESEARCHER, s.researcherAgents.contains(agent));
        }
        if (WAITING.equals(signal)) {
            d.put(MODEL, msg.path("metadata").path(MODEL).asText(""));
        }
        if (STAND_ASIDE.equals(signal)) {
            capacityDetails(d, msg.path("metadata"));
        }
        if (isTerminal(signal)) {
            putTime(d, LAST_SIGNAL_AT, s.lastSignalReceived);
        }
    }

    /** A stand-aside for want of an accelerator names the reason, and for a model too large the model. */
    private static void capacityDetails(ObjectNode d, JsonNode metadata) {
        String reason = metadata.path("reason").asText("");
        if (!DiscussionOrchestrator.isCapacityReason(reason)) {
            return;
        }
        d.put(CAPACITY_REASON, reason);
        String model = metadata.path(MODEL).asText("");
        if (DiscussionOrchestrator.REASON_MODEL_TOO_LARGE.equals(reason) && !model.isEmpty()) {
            d.put(TOO_LARGE_MODEL, model);
        }
    }

    private static String keyed(ThreadState s, String key) {
        return s.threadId + "/" + s.trace.round() + "/" + key;
    }

    /**
     * What tells this agent's messages apart in an event id: the message id, else the
     * message's timestamp, else a count, so two signals from one agent never share an id.
     */
    private static String messageKey(ThreadState s, String agent, String messageId, JsonNode msg) {
        if (!messageId.isEmpty()) {
            return messageId;
        }
        String timestamp = JsonFields.text(msg, "timestamp");
        return timestamp.isEmpty() ? "n" + s.trace.enter("anonymous/" + agent) : timestamp;
    }

    /**
     * Appends the event, stating the last sequence this writer saw on the thread's subject,
     * and returns whether it is in the log. A refusal for a wrong last sequence fences the
     * thread; any other failure is counted and leaves the thread as it was.
     */
    private boolean emit(ThreadState s, String type, String eventId, ObjectNode data) {
        var event = new CoordinatorEvent(eventId, type, s.threadId, s.trace.round(), clock.get(), coordinator, data);
        AppendResult result = eventLog.append(subjectFor.apply(s.crew, s.threadId), event, s.trace.lastSequence());
        if (result.isAppended()) {
            s.trace.appended(event, result.sequence());
            return true;
        }
        if (result.isFenced()) {
            s.trace.fence();
            metrics.logEventFenced();
            log.warn("Thread {} is written by another coordinator (event {} was refused): this coordinator stops acting on it",
                    s.threadId, eventId);
            return false;
        }
        metrics.logAppendFailed();
        markUnlogged(s, eventId);
        return true;
    }

    /**
     * The thread's log is incomplete from {@code eventId} on: it is not resumable, so no
     * more events are appended, and the discussion carries on without the log. Reported
     * once per thread.
     */
    private void markUnlogged(ThreadState s, String eventId) {
        if (s.trace.markUnlogged(eventId)) {
            metrics.threadUnlogged();
            log.warn("Coordinator log of thread {} is incomplete from event {}: the thread is not resumable and "
                    + "no more is logged for it", s.threadId, eventId);
        }
    }

    private static void putText(ObjectNode d, String field, String value) {
        if (value == null) {
            d.putNull(field);
        } else {
            d.put(field, value);
        }
    }

    private static void putTime(ObjectNode d, String field, Instant value) {
        putText(d, field, value == null ? null : value.toString());
    }

    private ArrayNode strings(Collection<String> values) {
        ArrayNode array = mapper.createArrayNode();
        if (values != null) {
            Collection<String> ordered = values instanceof Set ? new TreeSet<>(values) : values;
            ordered.forEach(array::add);
        }
        return array;
    }
}
