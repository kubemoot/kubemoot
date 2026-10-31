package ai.kubemoot.agent.nats;

import java.time.Instant;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

/**
 * What the coordinator's log can say about a thread: the part of the orchestrator's
 * per-thread state that a decision event records. Two threads with equal views are in
 * the same place for every purpose a restart cares about. The orchestrator's state and
 * the reducer's state both project onto it, so a test can compare them value for value.
 *
 * <p>Sets and maps are sorted copies, so equality does not depend on the collection
 * type or the order agents arrived in. Message content is not part of the view: the log
 * names the message ({@code messageId}) and the bus keeps the text. A heartbeat moves an
 * agent's deadline without an event, so deadlines in the view are the last logged ones.
 */
record ThreadView(
        Ids ids,
        Position position,
        Selection selection,
        Roster roster,
        Signals signals,
        Capacity capacity,
        Tokens tokens) {

    /** Who and what the thread is about. */
    record Ids(String threadId, String crew, String conversationId, String userQuery,
               String primaryChannel, Instant openedAt) {}

    /** Where the thread is in the phase machine. */
    record Position(int round, String phase, String phaseBeforePause, Instant phaseStartedAt,
                    boolean advisoryPending, Instant lastSignalAt) {}

    /** The subcommittee the coordinator convened. */
    record Selection(Set<String> innerCircle, double triageConfidence, Set<String> selectedSkills) {}

    /** What the current phase waits for. */
    record Roster(Set<String> members, Set<String> signalled, Map<String, Long> pending) {}

    /** The signals applied so far, by agent. */
    record Signals(Set<String> agree, Set<String> concern, Set<String> block, Set<String> standAside,
                   Set<String> failure, Set<String> failedSources, Set<String> researchers) {}

    /** Why agents could not answer for want of an accelerator. */
    record Capacity(Map<String, String> standAsides, Set<String> tooLargeModels,
                    Map<String, String> waiting, boolean synthesisPromptTooLarge) {}

    /** What the coordinator's own model calls cost. */
    record Tokens(long advisoryIn, long advisoryOut, long triageIn, long triageOut,
                  long synthesisIn, long synthesisOut) {}

    /** A sorted copy of {@code values}; empty for null. */
    static Set<String> sorted(java.util.Collection<String> values) {
        return values == null ? new TreeSet<>() : new TreeSet<>(values);
    }

    /** A sorted copy of {@code map}; empty for null. */
    static <V> Map<String, V> sorted(Map<String, V> map) {
        return map == null ? new TreeMap<>() : new TreeMap<>(map);
    }

    /** The view of the orchestrator's live state. */
    static ThreadView of(DiscussionOrchestrator.ThreadState s) {
        return new ThreadView(
                new Ids(s.threadId, s.crew, s.conversationId, s.userQuery, s.primaryChannel, s.threadCreated),
                new Position(s.trace.round(), s.phase.name(),
                        s.phaseBeforePause == null ? null : s.phaseBeforePause.name(),
                        s.phaseStarted, s.advisoryPending.get(), s.lastSignalReceived),
                new Selection(sorted(s.innerCircle), s.triageConfidence, sorted(s.selectedSkills)),
                new Roster(sorted(s.phaseRoster.get().members()), sorted(s.phaseRoster.get().signalled()),
                        sorted(s.pendingEvaluations)),
                new Signals(sorted(s.agreeSignals.keySet()), sorted(s.concernSignals.keySet()),
                        sorted(s.blockSignals.keySet()), sorted(s.standAsideSignals),
                        sorted(s.failureSignals.keySet()), sorted(s.failedSources),
                        sorted(s.researcherAgents)),
                new Capacity(sorted(s.capacityStandAsides), sorted(s.tooLargeModels),
                        sorted(s.waitingAgents), s.synthesisPromptTooLarge),
                new Tokens(s.advisoryInputTokens, s.advisoryOutputTokens, s.triageInputTokens,
                        s.triageOutputTokens, s.synthesisInputTokens, s.synthesisOutputTokens));
    }
}
