package ai.kubemoot.agent.nats;

import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
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
               String primaryChannel, Instant openedAt) {
        void differences(Ids live, List<String> out) {
            compare(out, "ids.threadId", threadId, live.threadId);
            compare(out, "ids.crew", crew, live.crew);
            compare(out, "ids.conversationId", conversationId, live.conversationId);
            compare(out, "ids.userQuery", userQuery, live.userQuery);
            compare(out, "ids.primaryChannel", primaryChannel, live.primaryChannel);
            compare(out, "ids.openedAt", openedAt, live.openedAt);
        }
    }

    /** Where the thread is in the phase machine. */
    record Position(int round, String phase, String phaseBeforePause, Instant phaseStartedAt,
                    boolean advisoryPending, Instant lastSignalAt) {
        void differences(Position live, List<String> out) {
            compare(out, "position.round", round, live.round);
            compare(out, "position.phase", phase, live.phase);
            compare(out, "position.phaseBeforePause", phaseBeforePause, live.phaseBeforePause);
            compare(out, "position.phaseStartedAt", phaseStartedAt, live.phaseStartedAt);
            compare(out, "position.advisoryPending", advisoryPending, live.advisoryPending);
            compare(out, "position.lastSignalAt", lastSignalAt, live.lastSignalAt);
        }
    }

    /** The subcommittee the coordinator convened. */
    record Selection(Set<String> innerCircle, double triageConfidence, Set<String> selectedSkills) {
        void differences(Selection live, List<String> out) {
            compare(out, "selection.innerCircle", innerCircle, live.innerCircle);
            compare(out, "selection.triageConfidence", triageConfidence, live.triageConfidence);
            compare(out, "selection.selectedSkills", selectedSkills, live.selectedSkills);
        }
    }

    /** What the current phase waits for. */
    record Roster(Set<String> members, Set<String> signalled, Map<String, Long> pending) {
        void differences(Roster live, List<String> out) {
            compare(out, "roster.members", members, live.members);
            compare(out, "roster.signalled", signalled, live.signalled);
            compare(out, "roster.pending", pending.keySet(), live.pending.keySet());
        }
    }

    /** The signals applied so far, by agent. */
    record Signals(Set<String> agree, Set<String> concern, Set<String> block, Set<String> standAside,
                   Set<String> failure, Set<String> failedSources, Set<String> researchers) {
        void differences(Signals live, List<String> out) {
            compare(out, "signals.agree", agree, live.agree);
            compare(out, "signals.concern", concern, live.concern);
            compare(out, "signals.block", block, live.block);
            compare(out, "signals.standAside", standAside, live.standAside);
            compare(out, "signals.failure", failure, live.failure);
            compare(out, "signals.failedSources", failedSources, live.failedSources);
            compare(out, "signals.researchers", researchers, live.researchers);
        }
    }

    /** Why agents could not answer for want of an accelerator. */
    record Capacity(Map<String, String> standAsides, Set<String> tooLargeModels,
                    Map<String, String> waiting, boolean synthesisPromptTooLarge) {
        void differences(Capacity live, List<String> out) {
            compare(out, "capacity.standAsides", standAsides, live.standAsides);
            compare(out, "capacity.tooLargeModels", tooLargeModels, live.tooLargeModels);
            compare(out, "capacity.waiting", waiting, live.waiting);
            compare(out, "capacity.synthesisPromptTooLarge", synthesisPromptTooLarge, live.synthesisPromptTooLarge);
        }
    }

    /** What the coordinator's own model calls cost. */
    record Tokens(long advisoryIn, long advisoryOut, long triageIn, long triageOut,
                  long synthesisIn, long synthesisOut) {
        void differences(Tokens live, List<String> out) {
            compare(out, "tokens.advisory", advisoryIn + "/" + advisoryOut, live.advisoryIn + "/" + live.advisoryOut);
            compare(out, "tokens.triage", triageIn + "/" + triageOut, live.triageIn + "/" + live.triageOut);
            compare(out, "tokens.synthesis", synthesisIn + "/" + synthesisOut,
                    live.synthesisIn + "/" + live.synthesisOut);
        }
    }

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

    /**
     * This view with every pending deadline set to 0. A heartbeat moves an agent's live
     * deadline without an event, so deadlines are not comparable between the live state
     * and the log; who is pending is.
     */
    ThreadView withoutDeadlines() {
        var pending = new TreeMap<String, Long>();
        roster.pending().keySet().forEach(agent -> pending.put(agent, 0L));
        return new ThreadView(ids, position, selection,
                new Roster(roster.members(), roster.signalled(), pending), signals, capacity, tokens);
    }

    /**
     * The fields in which this view (what the log says) differs from {@code live}, each as
     * {@code section.field: log=... live=...}; empty when they agree. Deadlines are not compared.
     */
    List<String> differences(ThreadView live) {
        var out = new ArrayList<String>();
        ids.differences(live.ids, out);
        position.differences(live.position, out);
        selection.differences(live.selection, out);
        roster.differences(live.roster, out);
        signals.differences(live.signals, out);
        capacity.differences(live.capacity, out);
        tokens.differences(live.tokens, out);
        return out;
    }

    /** Adds {@code field: log=.. live=..} to {@code out} when the two values differ. */
    private static void compare(List<String> out, String field, Object log, Object live) {
        if (!java.util.Objects.equals(log, live)) {
            out.add(field + ": log=" + log + " live=" + live);
        }
    }
}
