package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** What a ratifier's reply means, and the policy the archetype declares. */
class RatifyReplyTest {

    @Test
    void aReplyOpensWithItsVerdictAndTheRestIsTheReason() {
        var concern = RatifyReply.parse("CONCERN: the draft names crew-node-01, which my results do not hold");
        assertEquals(RatifyReply.Kind.CONCERN, concern.kind());
        assertEquals("the draft names crew-node-01, which my results do not hold", concern.text());

        assertEquals(RatifyReply.Kind.BLOCK, RatifyReply.parse("Block - 14 nodes, not 15").kind());
        assertEquals("14 nodes, not 15", RatifyReply.parse("Block - 14 nodes, not 15").text());
        assertEquals(RatifyReply.Kind.AGREE, RatifyReply.parse("  agree\nchecked all 14 rows").kind());
        assertEquals("checked all 14 rows", RatifyReply.parse("  agree\nchecked all 14 rows").text());
        assertEquals(RatifyReply.Kind.AGREE, RatifyReply.parse("**AGREED**: ok").kind());
    }

    @Test
    void aReplyThatOpensWithNoVerdictIsNotOne() {
        assertNull(RatifyReply.parse("I looked at the draft and it seems fine"));
        assertNull(RatifyReply.parse("Disagree: no"));
        assertNull(RatifyReply.parse("agreement is near"));
        assertNull(RatifyReply.parse(""));
        assertNull(RatifyReply.parse(null));
    }

    @Test
    void onlyAConcernAndABlockObject() {
        assertTrue(RatifyReply.Kind.CONCERN.objects());
        assertTrue(RatifyReply.Kind.BLOCK.objects());
        assertFalse(RatifyReply.Kind.AGREE.objects());
        assertFalse(RatifyReply.Kind.FAILURE.objects());
        assertFalse(RatifyReply.Kind.STAND_ASIDE.objects());
    }

    @Test
    void aSignalNameMapsToItsKind() {
        assertEquals(RatifyReply.Kind.FAILURE, RatifyReply.Kind.ofSignal("failure"));
        assertEquals(RatifyReply.Kind.STAND_ASIDE, RatifyReply.Kind.ofSignal("decline"));
        assertNull(RatifyReply.Kind.ofSignal("heartbeat"));
        assertNull(RatifyReply.Kind.ofSignal(null));
    }

    @Test
    void anArchetypeThatDeclaresNothingRatifiesTwoRounds() {
        var policy = RatifyPolicy.of(Optional.empty(), Optional.empty(), Optional.empty());

        assertEquals(RatifyPolicy.DEFAULT, policy);
        assertTrue(policy.enabled());
        assertEquals(2, policy.maxRounds());
        assertEquals(RatifyPolicy.Roster.CONTRIBUTORS, policy.roster());
    }

    @Test
    void theDeclaredValuesAreReadAndTheRoundsAreHeldInRange() {
        assertFalse(RatifyPolicy.of(Optional.of("false"), Optional.empty(), Optional.empty()).enabled());
        assertTrue(RatifyPolicy.of(Optional.of("yes"), Optional.empty(), Optional.empty()).enabled(),
                "only false turns it off");
        assertEquals(1, RatifyPolicy.of(Optional.empty(), Optional.of(0), Optional.empty()).maxRounds());
        assertEquals(5, RatifyPolicy.of(Optional.empty(), Optional.of(99), Optional.empty()).maxRounds());
        assertEquals(RatifyPolicy.Roster.CREW, RatifyPolicy.of(Optional.empty(), Optional.empty(), Optional.of("crew")).roster());
        assertEquals(RatifyPolicy.Roster.CONTRIBUTORS,
                RatifyPolicy.of(Optional.empty(), Optional.empty(), Optional.of("nonsense")).roster());
    }

    @Test
    void aRevisionIsAllowedWhileRoundsRemain() {
        var policy = RatifyPolicy.DEFAULT;
        assertTrue(policy.allowsRevisionAfter(1));
        assertFalse(policy.allowsRevisionAfter(2));
    }
}
