package ai.kubemoot.agent.nats;

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
import java.util.function.Supplier;

import static ai.kubemoot.agent.nats.CoordinatorEvent.*;
import static ai.kubemoot.agent.nats.CoordinatorWire.*;

/**
 * Writes the coordinator's decisions to the thread's log. The orchestrator calls one
 * method here after each decision, passing the thread; the recorder reads the thread's
 * state after the decision and writes what the decision changed, so the orchestrator
 * holds no event-building code. With the event log off every method returns at once.
 *
 * <p>Nothing here changes what a discussion does. Each method reads state, builds an
 * event and hands it to the log inside one guard: whatever goes wrong while building or
 * writing is logged and the event is dropped, and never reaches the orchestrator. The
 * events of one thread are built and handed over one at a time, in order.
 */
final class CoordinatorEventRecorder {

    /** The setting that turns the log on: environment variable {@code KUBEMOOT_COORDINATOR_EVENT_LOG}. */
    static final String CONFIG_KEY = "kubemoot.coordinator.event-log";

    private static final Logger log = LoggerFactory.getLogger(CoordinatorEventRecorder.class);

    private final boolean enabled;
    private final CoordinatorEventLog eventLog;
    private final BiFunction<String, String, String> subjectFor;
    private final String coordinator;
    private final ObjectMapper mapper;
    private final Supplier<Instant> clock;

    CoordinatorEventRecorder(boolean enabled, CoordinatorEventLog eventLog,
                             BiFunction<String, String, String> subjectFor, String coordinator,
                             ObjectMapper mapper, Supplier<Instant> clock) {
        this.enabled = enabled;
        this.eventLog = eventLog;
        this.subjectFor = subjectFor;
        this.coordinator = coordinator;
        this.mapper = mapper;
        this.clock = clock;
    }

    /** A recorder that writes nothing. */
    static CoordinatorEventRecorder off() {
        return new CoordinatorEventRecorder(false, CoordinatorEventLog.OFF, (crew, id) -> id, "", new ObjectMapper(),
                Instant::now);
    }

    /** The recorder for this runtime: on only when {@link #CONFIG_KEY} is true. */
    static CoordinatorEventRecorder fromConfig(String coordinator, NatsConnectionProvider natsProvider,
                                               ObjectMapper mapper) {
        boolean on = ConfigProvider.getConfig().getOptionalValue(CONFIG_KEY, Boolean.class).orElse(false);
        if (!on) {
            return off();
        }
        log.info("Coordinator event log is on: decisions are written to {}", CrewScope.COORDINATOR_STREAM);
        return new CoordinatorEventRecorder(true, new NatsCoordinatorEventLog(natsProvider, mapper),
                (crew, threadId) -> natsProvider.scope().forCrew(crew).coordinatorSubject(threadId),
                coordinator, mapper, Instant::now);
    }

    boolean enabled() {
        return enabled;
    }

    // --- decisions, in the order a thread meets them ---

    /** The thread exists: started by a request this coordinator took, or by an external {@code thread_start}. */
    void opened(ThreadState s, String source) {
        if (!enabled) {
            return;
        }
        s.trace.markOpened();
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            putText(d, "crew", s.crew);
            putText(d, "conversationId", s.conversationId);
            putText(d, "userQuery", s.userQuery);
            putText(d, "primaryChannel", s.primaryChannel);
            d.put("source", source);
            d.put("openedAt", s.threadCreated.toString());
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            emit(s, THREAD_OPENED, s.threadId + "/opened", d);
        });
    }

    /** The thread moved to its current phase (anything but closed, which has its own event). */
    void phaseEntered(ThreadState s, String reason) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE, s.phase.name());
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            putText(d, "phaseBeforePause", s.phaseBeforePause == null ? null : s.phaseBeforePause.name());
            d.put(ADVISORY_PENDING, s.advisoryPending.get());
            d.put(REASON, reason);
            emit(s, PHASE_ENTERED, keyed(s, "phase/" + s.phase.name() + "/" + s.trace.enter(s.phase.name())), d);
        });
    }

    /** The advisory step chose who to convene, by reasoning, by a fallback, or not at all (everyone may take part). */
    void subcommitteeConvened(ThreadState s, DiscussionOrchestrator.ReasoningResult reasoning,
                              DiscussionOrchestrator.AdvisoryOutcome outcome) {
        write(s, () -> {
            boolean byReasoning = reasoning != null && !reasoning.selected().isEmpty();
            subcommittee(s, byReasoning ? "reasoning" : fallbackPath(s), outcome.wisdom(), outcome.technologies());
        });
    }

    /** The advisory step failed: no subcommittee was chosen and every agent may take part. */
    void advisoryFailed(ThreadState s) {
        write(s, () -> subcommittee(s, "broadcast", null, List.of()));
    }

    /** The coordinator chose no toolers and answered from its own reasoning. */
    void answeredDirectly(ThreadState s, DiscussionOrchestrator.ReasoningResult reasoning) {
        write(s, () -> subcommittee(s, "direct", reasoning.brief(), reasoning.technologies()));
    }

    /** The advisory step ended and the evaluation clock reset: signals cleared, roster set. */
    void evaluationStarted(ThreadState s) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.set(ROSTER, strings(s.phaseRoster.get().members()));
            d.put("startedAt", s.phaseStarted.toString());
            emit(s, EVALUATION_STARTED, keyed(s, "evaluation/" + s.trace.enter(EVALUATION_STARTED)), d);
        });
    }

    /** Review began: the analysts it woke, each pending with a deadline. */
    void reviewStarted(ThreadState s) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.set(ROSTER, strings(s.phaseRoster.get().members()));
            ObjectNode pending = d.putObject(PENDING);
            new TreeMap<>(s.pendingEvaluations).forEach(pending::put);
            emit(s, REVIEW_STARTED, keyed(s, "review/" + s.trace.enter(REVIEW_STARTED)), d);
        });
    }

    /**
     * An agent's signal was applied to the thread. Records the signals that change state
     * (not a heartbeat, which only refreshes a deadline); a failure is its own event.
     */
    void signalApplied(ThreadState s, String agent, String messageType, JsonNode message) {
        write(s, () -> {
            JsonNode msg = message == null ? MissingNode.getInstance() : message;
            if (FAILURE.equals(messageType)) {
                failure(s, agent, msg, false);
                return;
            }
            String signal = signalFor(messageType);
            if (signal != null) {
                signal(s, agent, signal, msg);
            }
        });
    }

    /** A waking or ready signal from an agent starting up put it on the thread's pending list. */
    void wake(ThreadState s, String agent, String signal, long deadlineAt) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(AGENT, agent);
            d.put(SIGNAL, signal);
            d.put(DEADLINE_AT, deadlineAt);
            emit(s, SIGNAL_APPLIED, keyed(s, "signal/" + agent + "/" + signal + "/" + deadlineAt), d);
        });
    }

    /**
     * An agent's deadline passed and the coordinator recorded a failure for it, as it did
     * for an agent that sent none.
     */
    void timedOut(ThreadState s, String agent) {
        write(s, () -> failure(s, agent, MissingNode.getInstance(), true));
    }

    /** A phase ended because its participants settled; {@code basis} says why. */
    void settled(ThreadState s, String basis) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE, s.phase.name());
            d.put("basis", basis);
            ObjectNode counts = d.putObject("counts");
            counts.put(AGREE, s.agreeSignals.size());
            counts.put(CONCERN, s.concernSignals.size());
            counts.put("standAside", s.standAsideSignals.size());
            counts.put(BLOCK, s.blockSignals.size());
            counts.put(FAILURE, s.failureSignals.size());
            emit(s, SETTLED, keyed(s, "settled/" + s.phase.name() + "/" + s.trace.enter(SETTLED + s.phase.name())), d);
        });
    }

    /** The synthesis call is about to begin. */
    void synthesisStarted(ThreadState s) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put("agrees", s.agreeSignals.size());
            d.put("concerns", s.concernSignals.size());
            d.put("standAsides", s.standAsideSignals.size());
            emit(s, SYNTHESIS_STARTED, keyed(s, "synthesis/started/" + s.trace.enter(SYNTHESIS_STARTED)), d);
        });
    }

    /**
     * The answer exists and is about to be published. {@code outcome} is how it came to
     * be: {@code synthesized}, {@code capacity}, {@code direct}, {@code fallback} or
     * {@code ceiling}.
     */
    void synthesisDone(ThreadState s, String outcome, String channel, String response) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put("outcome", outcome);
            d.put(CHANNEL, channel);
            d.put("response", DiscussionOrchestrator.publishedContent("synthesis", response));
            ObjectNode tokens = d.putObject("tokens");
            tokens.put("in", s.synthesisInputTokens);
            tokens.put("out", s.synthesisOutputTokens);
            d.put("promptTooLarge", s.synthesisPromptTooLarge);
            emit(s, SYNTHESIS_DONE, keyed(s, "synthesis/done/" + s.trace.enter(SYNTHESIS_DONE)), d);
        });
    }

    /** A human reply reopened a closed thread: a new round begins with no roster and no signals. */
    void reopened(ThreadState s) {
        write(s, () -> {
            int round = s.trace.nextRound();
            ObjectNode d = mapper.createObjectNode();
            d.put(PHASE_STARTED_AT, s.phaseStarted.toString());
            d.set(ROSTER, mapper.createArrayNode());
            emit(s, THREAD_REOPENED, s.threadId + "/reopened/" + round, d);
        });
    }

    /** The thread is closed; {@code reason} says how (an outcome of {@link #synthesisDone}, {@code zombie} or {@code no_signals}). */
    void closed(ThreadState s, String reason, String channel) {
        write(s, () -> {
            ObjectNode d = mapper.createObjectNode();
            d.put(REASON, reason);
            d.put(CHANNEL, channel);
            d.put(ADVISORY_PENDING, s.advisoryPending.get());
            emit(s, THREAD_CLOSED, keyed(s, "closed"), d);
        });
    }

    // --- building and writing ---

    /**
     * Runs {@code body} when the log is on and the thread was opened, one event at a time
     * per thread, and keeps whatever it throws from reaching the orchestrator.
     */
    private void write(ThreadState s, Runnable body) {
        if (!enabled || !s.trace.opened()) {
            return;
        }
        try {
            synchronized (s.trace.lock()) {
                body.run();
            }
        } catch (RuntimeException e) {
            log.warn("A coordinator event for thread {} was not written: {}", s.threadId, e.toString());
        }
    }

    private void subcommittee(ThreadState s, String path, String brief, List<String> technologies) {
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
        emit(s, SUBCOMMITTEE_CONVENED, keyed(s, "subcommittee/" + s.trace.enter(SUBCOMMITTEE_CONVENED)), d);
    }

    /** A signal that changes state: the agent, its message, and the deadline and details that go with the kind. */
    private void signal(ThreadState s, String agent, String signal, JsonNode msg) {
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
        emit(s, SIGNAL_APPLIED, keyed(s, "signal/" + agent + "/" + messageKey(s, agent, messageId, msg)), d);
    }

    /**
     * A failure: reported by the agent ({@code msg} is its message), or recorded by the
     * coordinator when the agent's deadline passed ({@code synthesized}, no message).
     */
    private void failure(ThreadState s, String agent, JsonNode msg, boolean synthesized) {
        ObjectNode d = mapper.createObjectNode();
        d.put(AGENT, agent);
        d.put(CAUSE, synthesized ? "evaluation_timeout" : "reported");
        d.put(SYNTHESIZED, synthesized);
        d.set(FAILED_SOURCES, strings(synthesized ? List.of() : DiscussionOrchestrator.failedSourceNames(msg)));
        String messageId = synthesized ? "" : JsonFields.text(msg, MESSAGE_ID);
        d.put(MESSAGE_ID, messageId);
        putTime(d, LAST_SIGNAL_AT, s.lastSignalReceived);
        String key = synthesized ? "timeout" : messageKey(s, agent, messageId, msg);
        emit(s, FAILURE_RECORDED, keyed(s, "failure/" + agent + "/" + key), d);
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

    private void emit(ThreadState s, String type, String eventId, ObjectNode data) {
        var event = new CoordinatorEvent(eventId, type, s.threadId, s.trace.round(), clock.get(), coordinator, data);
        eventLog.append(subjectFor.apply(s.crew, s.threadId), event);
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
