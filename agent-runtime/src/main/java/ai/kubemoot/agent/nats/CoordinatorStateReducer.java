package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;

import java.time.Instant;
import java.util.Map;
import java.util.function.BiConsumer;

import static ai.kubemoot.agent.nats.CoordinatorEvent.*;
import static ai.kubemoot.agent.nats.CoordinatorWire.*;

/**
 * Rebuilds a thread's state from the events in its coordinator log. Pure: no clock, no
 * bus, no model. Each event type has one handler that does to the state exactly what the
 * orchestrator did when it wrote the event.
 *
 * <p>An event whose id was already applied is ignored (a duplicate or a replay), and so
 * is an event of a type this version does not know, so an older reader survives a newer
 * writer. An event for a different thread than the state's is ignored too.
 */
final class CoordinatorStateReducer {


    private static final Map<String, BiConsumer<CoordinatorThreadState, CoordinatorEvent>> HANDLERS = Map.ofEntries(
            Map.entry(THREAD_OPENED, CoordinatorStateReducer::opened),
            Map.entry(PHASE_ENTERED, CoordinatorStateReducer::phaseEntered),
            Map.entry(SUBCOMMITTEE_CONVENED, CoordinatorStateReducer::subcommitteeConvened),
            Map.entry(EVALUATION_STARTED, CoordinatorStateReducer::evaluationStarted),
            Map.entry(REVIEW_STARTED, CoordinatorStateReducer::reviewStarted),
            Map.entry(SIGNAL_APPLIED, CoordinatorStateReducer::signalApplied),
            Map.entry(FAILURE_RECORDED, CoordinatorStateReducer::failureRecorded),
            Map.entry(SETTLED, CoordinatorStateReducer::settled),
            Map.entry(SYNTHESIS_STARTED, CoordinatorStateReducer::synthesisStarted),
            Map.entry(SYNTHESIS_DONE, CoordinatorStateReducer::synthesisDone),
            Map.entry(THREAD_REOPENED, CoordinatorStateReducer::threadReopened),
            Map.entry(THREAD_CLOSED, CoordinatorStateReducer::threadClosed));

    private static final Map<String, BiConsumer<CoordinatorThreadState, CoordinatorEvent>> SIGNALS = Map.ofEntries(
            Map.entry(TRIAGING, CoordinatorStateReducer::triaging),
            Map.entry(EVALUATING, CoordinatorStateReducer::evaluating),
            Map.entry(WAITING, CoordinatorStateReducer::waiting),
            Map.entry(WAKING, CoordinatorStateReducer::deadlineOnly),
            Map.entry(READY, CoordinatorStateReducer::deadlineOnly),
            Map.entry(AGREE, CoordinatorStateReducer::agreed),
            Map.entry(CONCERN, CoordinatorStateReducer::concerned),
            Map.entry(BLOCK, CoordinatorStateReducer::blocked),
            Map.entry(STAND_ASIDE, CoordinatorStateReducer::stoodAside));

    private CoordinatorStateReducer() {
    }

    /** The state after {@code events}, applied in order, for the thread of the first one. */
    static CoordinatorThreadState reduce(Iterable<CoordinatorEvent> events) {
        var state = new CoordinatorThreadState();
        for (CoordinatorEvent event : events) {
            apply(state, event);
        }
        return state;
    }

    /**
     * Applies one event. Returns false, leaving the state as it was, when the event is a
     * repeat, is of an unknown type, or belongs to another thread.
     */
    static boolean apply(CoordinatorThreadState state, CoordinatorEvent event) {
        var handler = event == null ? null : HANDLERS.get(event.type());
        if (handler == null || !belongsTo(state, event) || !state.appliedEventIds.add(event.eventId())) {
            return false;
        }
        state.threadId = event.threadId();
        handler.accept(state, event);
        return true;
    }

    private static boolean belongsTo(CoordinatorThreadState state, CoordinatorEvent event) {
        return state.threadId == null || state.threadId.equals(event.threadId());
    }

    // --- handlers, one per event type ---

    private static void opened(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode d = e.data();
        s.crew = text(d, "crew");
        s.conversationId = text(d, "conversationId");
        s.userQuery = text(d, "userQuery");
        s.primaryChannel = text(d, "primaryChannel");
        s.source = text(d, "source");
        s.openedAt = instant(d, "openedAt");
        s.phaseStartedAt = instant(d, PHASE_STARTED_AT);
    }

    private static void phaseEntered(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode d = e.data();
        s.phase = text(d, PHASE);
        s.phaseStartedAt = instant(d, PHASE_STARTED_AT);
        s.phaseBeforePause = text(d, "phaseBeforePause");
        s.advisoryPending = d.path(ADVISORY_PENDING).asBoolean(false);
        if ("SYNTHESIZING".equals(s.phase)) {
            s.synthesisPromptTooLarge = false;
        }
    }

    private static void subcommitteeConvened(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode d = e.data();
        s.selectionPath = text(d, "path");
        s.brief = text(d, "brief");
        s.primaryChannel = text(d, "primaryChannel");
        s.triageConfidence = d.path("confidence").asDouble(-1.0);
        replaceAll(s.innerCircle, d.path("members"));
        replaceAll(s.selectedSkills, d.path("skills"));
        JsonNode tokens = d.path("tokens");
        s.advisoryIn = tokens.path("advisoryIn").asLong();
        s.advisoryOut = tokens.path("advisoryOut").asLong();
        s.triageIn = tokens.path("triageIn").asLong();
        s.triageOut = tokens.path("triageOut").asLong();
    }

    private static void evaluationStarted(CoordinatorThreadState s, CoordinatorEvent e) {
        s.clearSignals();
        s.lastSignalAt = null;
        s.phaseStartedAt = instant(e.data(), "startedAt");
        s.advisoryPending = false;
        newRoster(s, e.data().path(ROSTER));
    }

    private static void reviewStarted(CoordinatorThreadState s, CoordinatorEvent e) {
        s.pending.clear();
        e.data().path(PENDING).fields().forEachRemaining(entry -> s.pending.put(entry.getKey(),
                entry.getValue().asLong()));
        newRoster(s, e.data().path(ROSTER));
    }

    private static void newRoster(CoordinatorThreadState s, JsonNode members) {
        replaceAll(s.rosterMembers, members);
        s.rosterSignalled.clear();
    }

    private static void signalApplied(CoordinatorThreadState s, CoordinatorEvent e) {
        var handler = SIGNALS.get(text(e.data(), SIGNAL));
        if (handler != null) {
            handler.accept(s, e);
        }
    }

    private static void triaging(CoordinatorThreadState s, CoordinatorEvent e) {
        deadline(s, e);
    }

    private static void evaluating(CoordinatorThreadState s, CoordinatorEvent e) {
        deadline(s, e);
        s.waiting.remove(agent(e));
    }

    private static void waiting(CoordinatorThreadState s, CoordinatorEvent e) {
        s.waiting.put(agent(e), text(e.data(), MODEL) == null ? "" : text(e.data(), MODEL));
        deadline(s, e);
    }

    private static void deadlineOnly(CoordinatorThreadState s, CoordinatorEvent e) {
        deadline(s, e);
    }

    private static void agreed(CoordinatorThreadState s, CoordinatorEvent e) {
        endTurn(s, e);
        if (e.data().path(RESEARCHER).asBoolean(false)) {
            s.researchers.add(agent(e));
        }
        s.agree.put(agent(e), text(e.data(), MESSAGE_ID));
    }

    private static void concerned(CoordinatorThreadState s, CoordinatorEvent e) {
        endTurn(s, e);
        s.concern.put(agent(e), text(e.data(), MESSAGE_ID));
    }

    private static void blocked(CoordinatorThreadState s, CoordinatorEvent e) {
        endTurn(s, e);
        s.block.put(agent(e), text(e.data(), MESSAGE_ID));
    }

    private static void stoodAside(CoordinatorThreadState s, CoordinatorEvent e) {
        endTurn(s, e);
        s.standAside.add(agent(e));
        String reason = text(e.data(), CAPACITY_REASON);
        if (reason != null) {
            s.capacityStandAsides.put(agent(e), reason);
        }
        String tooLarge = text(e.data(), TOO_LARGE_MODEL);
        if (tooLarge != null) {
            s.tooLargeModels.add(tooLarge);
        }
    }

    private static void failureRecorded(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode d = e.data();
        s.pending.remove(agent(e));
        if (!d.path(SYNTHESIZED).asBoolean(false)) {
            s.waiting.remove(agent(e));
        }
        s.failure.put(agent(e), text(d, CAUSE));
        d.path(FAILED_SOURCES).forEach(n -> s.failedSources.add(n.asText()));
        s.lastSignalAt = instant(d, LAST_SIGNAL_AT);
        s.recordTerminal(agent(e));
    }

    private static void settled(CoordinatorThreadState s, CoordinatorEvent e) {
        s.lastSettledPhase = text(e.data(), PHASE);
        s.lastSettleBasis = text(e.data(), "basis");
    }

    private static void synthesisStarted(CoordinatorThreadState s, CoordinatorEvent e) {
        s.synthesisStarted = true;
    }

    private static void synthesisDone(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode d = e.data();
        s.synthesisOutcome = text(d, "outcome");
        s.answer = text(d, "response");
        s.synthesisPromptTooLarge = d.path("promptTooLarge").asBoolean(false);
        s.synthesisIn = d.path("tokens").path("in").asLong();
        s.synthesisOut = d.path("tokens").path("out").asLong();
    }

    private static void threadReopened(CoordinatorThreadState s, CoordinatorEvent e) {
        s.round = e.round();
        s.phase = "EVALUATING";
        s.phaseStartedAt = instant(e.data(), PHASE_STARTED_AT);
        s.clearSignals();
        newRoster(s, e.data().path(ROSTER));
        s.synthesisStarted = false;
        s.synthesisOutcome = null;
        s.answer = null;
        s.closeReason = null;
    }

    private static void threadClosed(CoordinatorThreadState s, CoordinatorEvent e) {
        s.phase = "CLOSED";
        s.closeReason = text(e.data(), REASON);
        s.advisoryPending = e.data().path(ADVISORY_PENDING).asBoolean(false);
    }

    // --- shared steps ---

    /** The step every terminal signal takes: the agent is no longer pending or waiting, and the phase roster hears it. */
    private static void endTurn(CoordinatorThreadState s, CoordinatorEvent e) {
        s.pending.remove(agent(e));
        s.waiting.remove(agent(e));
        s.lastSignalAt = instant(e.data(), LAST_SIGNAL_AT);
        s.recordTerminal(agent(e));
    }

    /** Sets the agent's deadline when the event carries one. */
    private static void deadline(CoordinatorThreadState s, CoordinatorEvent e) {
        JsonNode at = e.data().path(DEADLINE_AT);
        if (at.isNumber()) {
            s.pending.put(agent(e), at.asLong());
        }
    }

    private static String agent(CoordinatorEvent e) {
        return text(e.data(), AGENT);
    }

    /** The text of {@code field}, or null when it is absent or null. */
    static String text(JsonNode node, String field) {
        JsonNode value = node.path(field);
        return value.isMissingNode() || value.isNull() ? null : value.asText();
    }

    private static Instant instant(JsonNode node, String field) {
        String text = text(node, field);
        return text == null ? null : CoordinatorEvent.instant(text);
    }

    private static void replaceAll(java.util.Collection<String> target, JsonNode array) {
        target.clear();
        array.forEach(n -> target.add(n.asText()));
    }
}
