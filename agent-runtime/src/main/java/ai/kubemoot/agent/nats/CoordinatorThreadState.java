package ai.kubemoot.agent.nats;

import java.time.Instant;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;

/**
 * One thread's state as the coordinator's log rebuilds it. The same fields the
 * orchestrator keeps for a live thread, minus message content, plus what only the log
 * records (why the thread closed, what the answer was). Only
 * {@link CoordinatorStateReducer} changes it; {@link #view()} is the comparable part.
 */
final class CoordinatorThreadState {

    static final String PHASE_SUBMITTED = "SUBMITTED";

    // --- who and what ---
    String threadId;
    String crew;
    String conversationId;
    String userQuery;
    String primaryChannel;
    Instant openedAt;
    String source;

    // --- where it is ---
    int round;
    String phase = PHASE_SUBMITTED;
    String phaseBeforePause;
    Instant phaseStartedAt;
    boolean advisoryPending;
    Instant lastSignalAt;

    // --- the subcommittee ---
    final Set<String> innerCircle = new TreeSet<>();
    double triageConfidence = -1.0;
    final Set<String> selectedSkills = new TreeSet<>();
    String selectionPath;
    String brief;

    // --- what the phase waits for ---
    final Set<String> rosterMembers = new TreeSet<>();
    final Set<String> rosterSignalled = new TreeSet<>();
    final Map<String, Long> pending = new TreeMap<>();

    // --- signals, by agent (the value is the message that carried it) ---
    final Map<String, String> agree = new TreeMap<>();
    final Map<String, String> concern = new TreeMap<>();
    final Map<String, String> block = new TreeMap<>();
    final Set<String> standAside = new TreeSet<>();
    final Map<String, String> failure = new TreeMap<>();
    final Set<String> failedSources = new TreeSet<>();
    final Set<String> researchers = new TreeSet<>();
    final Map<String, String> capacityStandAsides = new TreeMap<>();
    final Set<String> tooLargeModels = new TreeSet<>();
    final Map<String, String> waiting = new TreeMap<>();
    boolean synthesisPromptTooLarge;

    // --- tokens of the coordinator's own calls ---
    long advisoryIn;
    long advisoryOut;
    long triageIn;
    long triageOut;
    long synthesisIn;
    long synthesisOut;

    // --- what only the log records ---
    String lastSettledPhase;
    String lastSettleBasis;
    boolean synthesisStarted;
    String synthesisOutcome;
    String answer;
    String closeReason;

    /** Ids already applied: a repeat of one is ignored. */
    final Set<String> appliedEventIds = new HashSet<>();

    /** True once the thread has closed. */
    boolean closed() {
        return "CLOSED".equals(phase);
    }

    /** Clears the signals and the agents still pending: a new evaluation round starts clean. */
    void clearSignals() {
        agree.clear();
        concern.clear();
        block.clear();
        standAside.clear();
        failure.clear();
        failedSources.clear();
        researchers.clear();
        capacityStandAsides.clear();
        tooLargeModels.clear();
        waiting.clear();
        pending.clear();
    }

    /** Records a terminal signal on the roster; an agent the phase does not wait for is ignored. */
    void recordTerminal(String agent) {
        if (rosterMembers.contains(agent)) {
            rosterSignalled.add(agent);
        }
    }

    /** The comparable part of the state. */
    ThreadView view() {
        return new ThreadView(
                new ThreadView.Ids(threadId, crew, conversationId, userQuery, primaryChannel, openedAt),
                new ThreadView.Position(round, phase, phaseBeforePause, phaseStartedAt, advisoryPending,
                        lastSignalAt),
                new ThreadView.Selection(ThreadView.sorted(innerCircle), triageConfidence,
                        ThreadView.sorted(selectedSkills)),
                new ThreadView.Roster(ThreadView.sorted(rosterMembers), ThreadView.sorted(rosterSignalled),
                        ThreadView.sorted(pending)),
                new ThreadView.Signals(ThreadView.sorted(agree.keySet()), ThreadView.sorted(concern.keySet()),
                        ThreadView.sorted(block.keySet()), ThreadView.sorted(standAside),
                        ThreadView.sorted(failure.keySet()), ThreadView.sorted(failedSources),
                        ThreadView.sorted(researchers)),
                new ThreadView.Capacity(ThreadView.sorted(capacityStandAsides),
                        ThreadView.sorted(tooLargeModels), ThreadView.sorted(waiting),
                        synthesisPromptTooLarge),
                new ThreadView.Tokens(advisoryIn, advisoryOut, triageIn, triageOut, synthesisIn,
                        synthesisOut));
    }
}
