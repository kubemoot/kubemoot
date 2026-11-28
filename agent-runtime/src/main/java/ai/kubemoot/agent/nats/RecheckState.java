package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * A thread's clarifying-question round: the questions asked, which are open, and how many
 * rounds the thread has used. A question goes from asked (relayed to the agent that made the
 * finding) to answered (the asker is woken with the revised finding) to settled (the asker
 * signalled again). The coordinator holds the phase while any question is not settled.
 *
 * <p>Pure state, no bus: the orchestrator decides with it and publishes what it decides. All
 * methods are synchronized, since signals arrive on the subscription thread and timeouts on the
 * phase checker.
 */
final class RecheckState {

    /** Why a question was not relayed and became a concern. */
    enum Refusal {
        /** The archetype allows no rounds (recheck.maxRounds is 0). */
        ROUND_OFF("the archetype allows no clarifying questions"),
        /** The thread has used its rounds. */
        ROUND_LIMIT("the thread has used its clarifying-question rounds"),
        /** The question names no agent, or one that is not in the crew. */
        UNKNOWN_AGENT("the question names no agent of the crew"),
        /** The question is addressed to the asker itself. */
        SELF("the question is addressed to the agent that asked it"),
        /** The agent asked is busy answering or asking another question. */
        BUSY("the agent asked is already in a clarifying question"),
        /** The thread is past the phases where findings are rechecked (or paused or closed). */
        PHASE("the discussion is not gathering or reviewing findings now");

        private final String text;

        Refusal(String text) {
            this.text = text;
        }

        String text() {
            return text;
        }

        String id() {
            return name().toLowerCase(java.util.Locale.ROOT);
        }
    }

    /** One question: its id, who asked whom, the question, and the revised finding once answered. */
    record Question(String id, String asker, String target, String text, String answer, String answerSignal) {
        Question answered(String signal, String finding) {
            return new Question(id, asker, target, text, finding, signal);
        }

        boolean isAnswered() {
            return answerSignal != null;
        }
    }

    /** The decision on a question: relayed, or refused for a reason. */
    record Decision(Question question, Refusal refusal) {
        boolean relayed() {
            return refusal == null;
        }
    }

    /** The most questions kept for the synthesis; a question past it is still decided, not kept. */
    static final int MAX_HISTORY = 20;

    private final Map<String, Question> openById = new HashMap<>();
    private final List<Question> history = new ArrayList<>();
    private int rounds;
    /** Every question decided this turn, kept or not, so each has its own id. */
    private int asked;

    /**
     * Decides on {@code asker}'s question to {@code target}: relayed when the thread has a round
     * left, the target is a known agent other than the asker, and neither is already in a question.
     */
    synchronized Decision ask(String idPrefix, String asker, String target, String text, int maxRounds,
                              Set<String> crew) {
        var question = new Question(idPrefix + "/q" + (asked + 1), asker, target == null ? "" : target,
                text == null ? "" : text, null, null);
        Refusal refusal = refusal(question, maxRounds, crew);
        register(question, refusal == null);
        return new Decision(question, refusal);
    }

    /** Records a question refused for {@code refusal} without checking it: kept for the synthesis, never open. */
    synchronized Decision refuse(String idPrefix, String asker, String target, String text, Refusal refusal) {
        var question = new Question(idPrefix + "/q" + (asked + 1), asker, target == null ? "" : target,
                text == null ? "" : text, null, null);
        register(question, false);
        return new Decision(question, refusal);
    }

    /** True when the question {@code questionId} is open, addressed to {@code agent}, and not yet answered. */
    synchronized boolean isOpenTo(String questionId, String agent) {
        var q = questionId == null ? null : openById.get(questionId);
        return q != null && !q.isAnswered() && q.target().equals(agent);
    }

    /**
     * Puts a question the coordinator log recorded back: relayed, it is open and uses a round;
     * refused, it is only kept for the synthesis. A restarted coordinator calls this.
     */
    synchronized void restore(Question question, boolean relayed) {
        register(question, relayed);
    }

    private void register(Question question, boolean relayed) {
        asked++;
        if (relayed) {
            rounds++;
            openById.put(question.id(), question);
        }
        if (history.size() < MAX_HISTORY) {
            history.add(question);
        }
    }

    /** Takes over {@code other}'s questions and rounds: the round as a restarted coordinator's log left it. */
    void copyFrom(RecheckState other) {
        var taken = other.snapshot();
        synchronized (this) {
            openById.clear();
            openById.putAll(taken.open());
            history.clear();
            history.addAll(taken.history());
            rounds = taken.rounds();
            asked = taken.asked();
        }
    }

    /** One consistent copy of the round, taken under this state's lock. */
    private record Snapshot(Map<String, Question> open, List<Question> history, int rounds, int asked) {
    }

    private synchronized Snapshot snapshot() {
        return new Snapshot(new HashMap<>(openById), new ArrayList<>(history), rounds, asked);
    }

    private Refusal refusal(Question q, int maxRounds, Set<String> crew) {
        if (maxRounds <= 0) {
            return Refusal.ROUND_OFF;
        }
        if (q.target().isEmpty() || crew == null || !crew.contains(q.target())) {
            return Refusal.UNKNOWN_AGENT;
        }
        if (q.target().equals(q.asker())) {
            return Refusal.SELF;
        }
        if (rounds >= maxRounds) {
            return Refusal.ROUND_LIMIT;
        }
        return involved(q.target()) || involved(q.asker()) ? Refusal.BUSY : null;
    }

    private boolean involved(String agent) {
        return openById.values().stream().anyMatch(q -> q.asker().equals(agent) || q.target().equals(agent));
    }

    /**
     * Records the answer to the question {@code questionId} from {@code agent}: its signal and
     * revised finding. Returns the answered question, or null when no open question with that id
     * is addressed to the agent (a late or repeated answer).
     */
    synchronized Question answer(String questionId, String agent, String signal, String finding) {
        var q = openById.get(questionId);
        if (q == null || q.isAnswered() || !q.target().equals(agent)) {
            return null;
        }
        var answered = q.answered(signal, finding);
        openById.put(questionId, answered);
        replaceInHistory(answered);
        return answered;
    }

    /** The asker signalled again after its question was answered: the question is settled. */
    synchronized Question settle(String asker) {
        for (var q : List.copyOf(openById.values())) {
            if (q.asker().equals(asker) && q.isAnswered()) {
                openById.remove(q.id());
                return q;
            }
        }
        return null;
    }

    /**
     * Removes and returns the open questions {@code agent} was asked and has not answered: it
     * failed or missed its deadline, so each becomes its asker's concern.
     */
    synchronized List<Question> abandonedBy(String agent) {
        var abandoned = new ArrayList<Question>();
        for (var q : List.copyOf(openById.values())) {
            boolean unanswered = !q.isAnswered() && q.target().equals(agent);
            boolean askerGone = q.asker().equals(agent);
            if (unanswered || askerGone) {
                openById.remove(q.id());
                if (unanswered) {
                    abandoned.add(q);
                }
            }
        }
        return abandoned;
    }

    /** True when {@code agent} was asked a question it has not answered yet. */
    synchronized boolean awaitsAnswerFrom(String agent) {
        return openById.values().stream().anyMatch(q -> !q.isAnswered() && q.target().equals(agent));
    }

    /** The question {@code agent} has asked and is waiting on, or null. */
    synchronized Question openFrom(String agent) {
        return openById.values().stream().filter(q -> q.asker().equals(agent)).findFirst().orElse(null);
    }

    /** True while any question is asked or answered but not yet settled by its asker. */
    synchronized boolean inProgress() {
        return !openById.isEmpty();
    }

    /** Every question of the round so far, relayed or not, in the order asked; answered ones carry the answer. */
    synchronized List<Question> history() {
        return List.copyOf(history);
    }

    /** A new turn starts with no questions and every round available. */
    synchronized void reset() {
        openById.clear();
        history.clear();
        rounds = 0;
        asked = 0;
    }

    private void replaceInHistory(Question answered) {
        for (int i = 0; i < history.size(); i++) {
            if (history.get(i).id().equals(answered.id())) {
                history.set(i, answered);
                return;
            }
        }
    }
}
