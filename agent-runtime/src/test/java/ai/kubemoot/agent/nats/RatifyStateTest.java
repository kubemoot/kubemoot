package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.time.Instant;
import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The rules of one thread's ratification, with no clock and no bus. */
class RatifyStateTest {

    private static final String A = "k8s-nodes";
    private static final String B = "compute";
    private static final String DRAFT = "14 nodes, all Ready.";

    private RatifyState state;

    @BeforeEach
    void begin() {
        state = new RatifyState();
        state.begin(DRAFT, List.of(A, B), Instant.EPOCH);
    }

    @Test
    void aReplyCountsOnlyForTheCurrentRoundAndOnlyFromTheRoster() {
        assertFalse(state.record(A, RatifyReply.Kind.AGREE, "ok", 2, state.draftId()), "another round");
        assertFalse(state.record(A, RatifyReply.Kind.AGREE, "ok", 1, "a-draft-before-a-restart"), "another draft");
        assertFalse(state.record("stranger", RatifyReply.Kind.AGREE, "ok", 1, state.draftId()), "not on the roster");
        assertTrue(state.record(A, RatifyReply.Kind.AGREE, "ok", 1, state.draftId()));
        assertFalse(state.record(A, RatifyReply.Kind.BLOCK, "changed my mind", 1, state.draftId()), "the first answer stands");
        assertFalse(state.everyoneAnswered());
        assertTrue(state.record(B, RatifyReply.Kind.AGREE, "ok", 1, state.draftId()));
        assertTrue(state.everyoneAnswered());
    }

    @Test
    void aTableThatAgreedIsRatifiedAndTheAnswerIsTheDraftAsItIs() {
        state.record(A, RatifyReply.Kind.AGREE, "", 1, state.draftId());
        state.record(B, RatifyReply.Kind.AGREE, "", 1, state.draftId());

        assertEquals(RatifyState.RATIFIED, state.outcome());
        assertEquals(DRAFT, state.releaseText());
        assertTrue(state.objections().isEmpty());
    }

    @Test
    void aConcernAndABlockAreObjectionsAndAFailureIsNot() {
        state.record(A, RatifyReply.Kind.CONCERN, "names a node that is not there", 1, state.draftId());
        state.record(B, RatifyReply.Kind.FAILURE, "deadline", 1, state.draftId());

        assertEquals(1, state.objections().size());
        assertEquals(List.of(B), state.failures());
        assertEquals(RatifyState.RELEASED_WITH_OBJECTIONS, state.outcome());
    }

    @Test
    void aReleasedAnswerNamesEveryStandingObjectionAndEveryAgentThatDidNotAnswer() {
        state.record(A, RatifyReply.Kind.BLOCK, "there are 14 nodes, not 15", 1, state.draftId());
        state.record(B, RatifyReply.Kind.FAILURE, "deadline", 1, state.draftId());

        String text = state.releaseText();

        assertTrue(text.startsWith(DRAFT), text);
        assertTrue(text.contains("Objections that stand:\n- k8s-nodes (block): there are 14 nodes, not 15"), text);
        assertTrue(text.contains("Not checked by these agents (they did not answer):\n- compute"), text);
    }

    @Test
    void anObjectionWithNoReasonIsStillShown() {
        state.record(A, RatifyReply.Kind.CONCERN, "", 1, state.draftId());
        state.record(B, RatifyReply.Kind.AGREE, "", 1, state.draftId());

        assertTrue(state.releaseText().contains("k8s-nodes (concern): no reason given"));
    }

    @Test
    void aFailureAloneIsNotRatifiedButIsNoObjection() {
        state.record(A, RatifyReply.Kind.AGREE, "", 1, state.draftId());
        state.record(B, RatifyReply.Kind.FAILURE, "deadline", 1, state.draftId());

        assertEquals(RatifyState.NOT_RATIFIED, state.outcome());
        assertTrue(state.objections().isEmpty());
    }

    @Test
    void aRevisionStartsTheNextRoundWithTheSameRosterAndKeepsWhatWasRaised() {
        state.record(A, RatifyReply.Kind.CONCERN, "15 is wrong", 1, state.draftId());
        state.record(B, RatifyReply.Kind.AGREE, "", 1, state.draftId());
        assertTrue(state.beginRevision());
        assertFalse(state.beginRevision(), "one revision at a time");

        state.revise("14 nodes, all Ready (not 15).");

        assertEquals(2, state.round());
        assertEquals("14 nodes, all Ready (not 15).", state.draft());
        assertEquals(java.util.Set.of(A, B), state.roster());
        assertFalse(state.everyoneAnswered(), "the new round is unanswered");
        assertEquals(1, state.answeredObjections().size());
        assertTrue(state.record(A, RatifyReply.Kind.AGREE, "", 2, state.draftId()));
        assertTrue(state.record(B, RatifyReply.Kind.AGREE, "", 2, state.draftId()));
        assertEquals(RatifyState.RATIFIED, state.outcome());
        assertEquals(1, ((List<?>) state.metadata().get("earlierObjections")).size());
    }

    @Test
    void aRevisionThatCannotBeWrittenLeavesTheDraftAndFreesTheClaim() {
        assertTrue(state.beginRevision());
        state.abandonRevision();
        assertTrue(state.beginRevision());
        assertEquals(DRAFT, state.draft());
    }

    @Test
    void aStopBeforeAnyoneAnsweredIsNotRatifiedAndNamesWhoDidNotAnswer() {
        assertEquals(RatifyState.NOT_RATIFIED, state.outcome());
        assertEquals(List.of(B, A), state.absent());
        assertTrue(state.releaseText().contains("Not checked by these agents (they did not answer):\n- compute\n- k8s-nodes"),
                state.releaseText());
        assertEquals(List.of(B, A), state.metadata().get("failures"));
    }

    @Test
    void aStopAfterSomeAnsweredNamesOnlyTheOthers() {
        state.record(A, RatifyReply.Kind.AGREE, "", 1, state.draftId());

        assertEquals(List.of(B), state.absent());
        assertEquals(RatifyState.NOT_RATIFIED, state.outcome());
    }

    @Test
    void aStaleDraftIdIsNeverCountedAfterARevision() {
        String first = state.draftId();
        state.record(A, RatifyReply.Kind.CONCERN, "wrong", 1, first);
        state.record(B, RatifyReply.Kind.AGREE, "", 1, first);
        assertTrue(state.beginRevision());
        assertTrue(state.revise("14 nodes."));

        assertFalse(state.record(A, RatifyReply.Kind.AGREE, "", 2, first), "a reply to the first draft");
        assertTrue(state.record(A, RatifyReply.Kind.AGREE, "", 2, state.draftId()));
    }

    @Test
    void aRevisionThatArrivesAfterTheReleaseIsRefused() {
        assertTrue(state.beginRevision());
        assertTrue(state.claimRelease());

        assertFalse(state.revise("too late"), "the draft was released meanwhile");
        assertEquals(DRAFT, state.draft());
        assertEquals(1, state.round());
    }

    @Test
    void noRevisionBeginsOnceTheDraftIsReleased() {
        assertTrue(state.claimRelease());
        assertFalse(state.beginRevision());
    }

    @Test
    void theReleaseIsClaimedOnce() {
        assertTrue(state.claimRelease());
        assertFalse(state.claimRelease());
    }

    @Test
    void resetForgetsTheRatification() {
        state.reset();

        assertFalse(state.active());
        assertFalse(state.everyoneAnswered());
    }

    @Test
    void metadataCarriesTheStateTheRoundsAndWhoSaidWhat() {
        state.record(A, RatifyReply.Kind.BLOCK, "wrong count", 1, state.draftId());
        state.record(B, RatifyReply.Kind.AGREE, "", 1, state.draftId());

        var meta = state.metadata();

        assertEquals("released_with_objections", meta.get("state"));
        assertEquals(1, meta.get("rounds"));
        assertEquals(List.of(A, B), meta.get("roster"));
        assertEquals(1, ((List<?>) meta.get("objections")).size());
        assertTrue(((List<?>) meta.get("failures")).isEmpty());
    }

    @Test
    void theRevisionPromptPutsTheEvidenceTheDraftAndTheObjectionsInFront() {
        state.record(A, RatifyReply.Kind.CONCERN, "15 is wrong", 1, state.draftId());
        String prompt = RatifyText.revisionPrompt("EVIDENCE", DRAFT, state.objections());

        assertTrue(prompt.startsWith("EVIDENCE"));
        assertTrue(prompt.contains("[DRAFT]\n" + DRAFT));
        assertTrue(prompt.contains("[OBJECTIONS]\n- k8s-nodes (concern): 15 is wrong"));
        assertFalse(prompt.toLowerCase().contains("workspace_"), "the protocol text names no tool");
        assertFalse(RatifyText.ratifierInstruction(false).toLowerCase().contains("workspace_"));
        assertEquals("[RATIFY]", RatifyText.ratifierInstruction(true), "the crew's module carries the wording");
    }
}
