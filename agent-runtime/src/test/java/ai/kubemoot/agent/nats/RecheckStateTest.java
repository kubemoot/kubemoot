package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

class RecheckStateTest {

    private static final String ADVISOR = "k8s-advisor";
    private static final String NODES = "k8s-nodes";
    private static final String STORAGE = "k8s-storage";
    private static final Set<String> CREW = Set.of(ADVISOR, NODES, STORAGE);
    private static final String QUESTION = "can you recheck the node count? 100 seems high for this homelab";

    private final RecheckState round = new RecheckState();

    private RecheckState.Decision ask(String asker, String target, int maxRounds) {
        return round.ask("t1/0", asker, target, QUESTION, maxRounds, CREW);
    }

    @Test
    void aQuestionToACrewAgentIsRelayedAndOpen() {
        var decision = ask(ADVISOR, NODES, 1);
        assertTrue(decision.relayed());
        assertEquals("t1/0/q1", decision.question().id());
        assertEquals(NODES, decision.question().target());
        assertTrue(round.inProgress());
        assertEquals(decision.question(), round.openFrom(ADVISOR));
    }

    @Test
    void theAnswerThenTheAskersSignalSettleTheQuestion() {
        var q = ask(ADVISOR, NODES, 1).question();
        assertNull(round.answer(q.id(), STORAGE, "agree", "x"), "only the agent asked answers");
        var answered = round.answer(q.id(), NODES, "concern", "7 nodes; the 100 was a pod count");
        assertEquals("concern", answered.answerSignal());
        assertNull(round.answer(q.id(), NODES, "agree", "again"), "a repeated answer is ignored");
        assertTrue(round.inProgress(), "the asker has not signalled again yet");
        assertNull(round.settle(NODES), "the agent asked does not settle the asker's question");
        assertEquals(q.id(), round.settle(ADVISOR).id());
        assertFalse(round.inProgress());
        assertEquals("7 nodes; the 100 was a pod count", round.history().get(0).answer());
    }

    @Test
    void theRoundLimitTurnsTheNextQuestionIntoAConcern() {
        var first = ask(ADVISOR, NODES, 1).question();
        round.answer(first.id(), NODES, "agree", "7 nodes");
        round.settle(ADVISOR);
        var second = ask(ADVISOR, STORAGE, 1);
        assertFalse(second.relayed());
        assertEquals(RecheckState.Refusal.ROUND_LIMIT, second.refusal());
        assertEquals(2, round.history().size(), "a refused question is kept for the synthesis");
    }

    @Test
    void zeroRoundsTurnsTheRoundOff() {
        assertEquals(RecheckState.Refusal.ROUND_OFF, ask(ADVISOR, NODES, 0).refusal());
        assertFalse(round.inProgress());
    }

    @Test
    void aQuestionToAnAgentNotInTheCrewOrToNoAgentIsRefused() {
        assertEquals(RecheckState.Refusal.UNKNOWN_AGENT, ask(ADVISOR, "k8s-nodez", 1).refusal());
        assertEquals(RecheckState.Refusal.UNKNOWN_AGENT, ask(ADVISOR, "", 1).refusal());
        assertEquals(RecheckState.Refusal.UNKNOWN_AGENT, round.ask("t1/0", ADVISOR, NODES, QUESTION, 1, null).refusal());
    }

    @Test
    void aQuestionToItselfIsRefused() {
        assertEquals(RecheckState.Refusal.SELF, ask(NODES, NODES, 1).refusal());
    }

    @Test
    void anAgentAlreadyInAQuestionCannotBeAskedAgain() {
        ask(ADVISOR, NODES, 3);
        assertEquals(RecheckState.Refusal.BUSY, ask(STORAGE, NODES, 3).refusal());
        assertEquals(RecheckState.Refusal.BUSY, ask(NODES, STORAGE, 3).refusal(), "the agent asked cannot ask");
    }

    @Test
    void anAgentThatDoesNotAnswerAbandonsItsQuestion() {
        var q = ask(ADVISOR, NODES, 1).question();
        var abandoned = round.abandonedBy(NODES);
        assertEquals(1, abandoned.size());
        assertEquals(q.id(), abandoned.get(0).id());
        assertFalse(round.inProgress());
    }

    @Test
    void anAskerThatGoesAwayLeavesNothingOpen() {
        var q = ask(ADVISOR, NODES, 1).question();
        round.answer(q.id(), NODES, "agree", "7 nodes");
        assertTrue(round.abandonedBy(ADVISOR).isEmpty(), "an answered question is not the asker's concern");
        assertFalse(round.inProgress());
    }

    @Test
    void aNewTurnStartsWithEveryRound() {
        ask(ADVISOR, NODES, 1);
        round.reset();
        assertFalse(round.inProgress());
        assertTrue(round.history().isEmpty());
        assertTrue(ask(ADVISOR, NODES, 1).relayed());
    }

    @Test
    void aRestoredRoundKeepsItsRoundsAndItsNumbering() {
        var logged = new RecheckState();
        logged.restore(new RecheckState.Question("t1/0/q1", ADVISOR, NODES, QUESTION, null, null), true);
        var live = new RecheckState();
        live.copyFrom(logged);
        assertTrue(live.awaitsAnswerFrom(NODES));
        assertFalse(live.awaitsAnswerFrom(STORAGE));
        assertEquals(RecheckState.Refusal.ROUND_LIMIT,
                live.ask("t1/0", STORAGE, ADVISOR, QUESTION, 1, CREW).refusal(), "the restored round counts");
        assertEquals("t1/0/q2", live.history().get(1).id(), "a new question does not reuse an id");
    }

    @Test
    void theHistoryForTheSynthesisIsCapped() {
        for (int i = 0; i < RecheckState.MAX_HISTORY + 5; i++) {
            round.ask("t1/0", ADVISOR, "nobody", QUESTION, 1, CREW);
        }
        assertEquals(RecheckState.MAX_HISTORY, round.history().size());
    }
}
