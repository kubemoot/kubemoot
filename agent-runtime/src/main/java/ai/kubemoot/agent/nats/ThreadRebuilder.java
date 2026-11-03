package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.nats.DiscussionOrchestrator.Phase;
import ai.kubemoot.agent.nats.DiscussionOrchestrator.ThreadState;
import com.fasterxml.jackson.databind.JsonNode;

import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;

/**
 * Builds the orchestrator's state for a thread from the state its log reduces to: the
 * reverse of {@code ThreadView.of}. The thread starts its clocks now, so the hard ceiling
 * and the zombie sweep count from the takeover and the time the thread had no coordinator
 * is not charged to it. What a signal said is not in the log, only which message carried it;
 * those texts are filled in from the bus by {@link #fillContent}.
 */
final class ThreadRebuilder {

    private static final String PLACEHOLDER = "";
    /** What the coordinator wrote for an agent whose deadline passed; the same words the live path uses. */
    private static final String TIMED_OUT = "Evaluation did not complete within estimated window";

    private ThreadRebuilder() {
    }

    /** The thread as its log left it. Pending deadlines are not set here ({@link DiscussionOrchestrator#rearmDeadlines}). */
    static ThreadState build(CoordinatorThreadState log) {
        var state = new ThreadState(log.threadId);
        state.crew = log.crew;
        state.conversationId = log.conversationId;
        state.userQuery = log.userQuery;
        state.primaryChannel = log.primaryChannel;
        state.requestSeq = log.requestSeq;
        state.discussSeq = log.discussSeq;
        state.phase = Phase.valueOf(log.phase);
        state.phaseBeforePause = log.phaseBeforePause == null ? null : Phase.valueOf(log.phaseBeforePause);
        state.phaseStarted = state.threadCreated;
        state.advisoryPending.set(log.advisoryPending);
        state.lastSignalReceived = log.lastSignalAt;
        state.advisoryContent = log.brief;
        state.advisoryTechnologies = new CopyOnWriteArrayList<>(log.technologies);
        selection(state, log);
        signals(state, log);
        capacity(state, log);
        state.advisoryInputTokens = log.advisoryIn;
        state.advisoryOutputTokens = log.advisoryOut;
        state.triageInputTokens = log.triageIn;
        state.triageOutputTokens = log.triageOut;
        state.synthesisInputTokens = log.synthesisIn;
        state.synthesisOutputTokens = log.synthesisOut;
        state.answer = log.answer;
        return state;
    }

    private static void selection(ThreadState state, CoordinatorThreadState log) {
        if (!log.innerCircle.isEmpty()) {
            state.innerCircle = ConcurrentHashMap.newKeySet();
            state.innerCircle.addAll(log.innerCircle);
        }
        state.triageConfidence = log.triageConfidence;
        if (!log.selectedSkills.isEmpty()) {
            state.selectedSkills = ConcurrentHashMap.newKeySet();
            state.selectedSkills.addAll(log.selectedSkills);
        }
        state.phaseRoster.set(PhaseRoster.restore(log.rosterMembers, log.rosterSignalled));
    }

    private static void signals(ThreadState state, CoordinatorThreadState log) {
        log.agree.keySet().forEach(agent -> state.agreeSignals.put(agent, PLACEHOLDER));
        log.concern.keySet().forEach(agent -> state.concernSignals.put(agent, PLACEHOLDER));
        log.block.keySet().forEach(agent -> state.blockSignals.put(agent, PLACEHOLDER));
        state.standAsideSignals.addAll(log.standAside);
        log.failure.forEach((agent, cause) ->
                state.failureSignals.put(agent, CoordinatorWire.CAUSE_TIMEOUT.equals(cause) ? TIMED_OUT : PLACEHOLDER));
        state.failedSources.addAll(log.failedSources);
        state.researcherAgents.addAll(log.researchers);
        state.dataAnalysts.addAll(log.dataAnalysts);
        log.readKeys.forEach((agent, keys) ->
                state.readKeys.computeIfAbsent(agent, a -> ConcurrentHashMap.newKeySet()).addAll(keys));
        state.convenings = log.convenings;
        state.convenedAnalysts.addAll(log.convenedAnalysts);
        state.convenedAssignments = log.convenedAssignments;
    }

    private static void capacity(ThreadState state, CoordinatorThreadState log) {
        state.capacityStandAsides.putAll(log.capacityStandAsides);
        state.tooLargeModels.addAll(log.tooLargeModels);
        state.waitingAgents.putAll(log.waiting);
        state.synthesisPromptTooLarge = log.synthesisPromptTooLarge;
    }

    /**
     * Puts the text of a bus message into the signal it carried, when the log says this
     * message carried the agent's signal. The synthesis prompt and the review summary are
     * built from these texts.
     */
    static void fillContent(ThreadState state, CoordinatorThreadState log, JsonNode msg) {
        String agent = JsonFields.text(msg, "agentName");
        String messageId = JsonFields.text(msg, "messageId");
        String content = JsonFields.text(msg, "content");
        if (agent.isEmpty() || messageId.isEmpty()) {
            return;
        }
        switch (JsonFields.text(msg, "messageType")) {
            case CoordinatorWire.AGREE, "contribution" -> putIf(state.agreeSignals, log.agree, agent, messageId, content);
            case CoordinatorWire.CONCERN -> putIf(state.concernSignals, log.concern, agent, messageId, content);
            case CoordinatorWire.BLOCK -> putIf(state.blockSignals, log.block, agent, messageId, content);
            case CoordinatorWire.FAILURE -> putFailure(state, log, agent, messageId, content);
            default -> {
                // Other messages carry no signal text.
            }
        }
    }

    private static void putIf(Map<String, String> live, Map<String, String> logged, String agent, String messageId,
                              String content) {
        if (messageId.equals(logged.get(agent)) && live.containsKey(agent)) {
            live.put(agent, content);
        }
    }

    private static void putFailure(ThreadState state, CoordinatorThreadState log, String agent, String messageId,
                                   String content) {
        boolean recorded = log.appliedMessageIds.contains(messageId);
        if (recorded && state.failureSignals.containsKey(agent) && !TIMED_OUT.equals(state.failureSignals.get(agent))) {
            state.failureSignals.put(agent, content);
        }
    }
}
