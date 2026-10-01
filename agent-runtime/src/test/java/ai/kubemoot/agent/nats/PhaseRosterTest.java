package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;

/** The settle guard: a phase's roster is complete only when every member has signalled in it. */
class PhaseRosterTest {

    private static final Map<String, Long> NONE_PENDING = Map.of();

    @Test
    void completes_onceEveryMemberHasRecordedATerminalSignal() {
        var roster = PhaseRoster.of(List.of("k8s-nodes", "k8s-metrics"));
        roster.recordTerminalSignal("k8s-nodes");
        assertFalse(roster.allSignalled(NONE_PENDING), "k8s-metrics has not signalled");
        roster.recordTerminalSignal("k8s-metrics");
        assertTrue(roster.allSignalled(NONE_PENDING));
    }

    @Test
    void aMemberStillPending_holdsTheRoster_evenAfterASignal() {
        var roster = PhaseRoster.of(List.of("k8s-nodes"));
        roster.recordTerminalSignal("k8s-nodes");
        var pending = new HashMap<String, Long>();
        pending.put("k8s-nodes", Long.MAX_VALUE);
        assertFalse(roster.allSignalled(pending));
    }

    @Test
    void anUnknownRoster_neverCompletes() {
        assertFalse(PhaseRoster.unknown().allSignalled(NONE_PENDING));
        assertFalse(PhaseRoster.of(List.of()).allSignalled(NONE_PENDING));
        assertFalse(PhaseRoster.of(null).allSignalled(NONE_PENDING));
        assertFalse(PhaseRoster.unknown().isKnown());
    }

    @Test
    void signalsFromNonMembersAndNull_areIgnored() {
        var roster = PhaseRoster.of(List.of("k8s-nodes"));
        roster.recordTerminalSignal("compute");
        roster.recordTerminalSignal(null);
        assertFalse(roster.allSignalled(NONE_PENDING));
    }

    @Test
    void aNewRoster_startsWithNothingSignalled() {
        var first = PhaseRoster.of(List.of("compute"));
        first.recordTerminalSignal("compute");
        var next = PhaseRoster.of(List.of("compute"));
        assertTrue(first.allSignalled(NONE_PENDING));
        assertFalse(next.allSignalled(NONE_PENDING), "a signal from an earlier phase does not carry over");
    }

    @Test
    void members_areACopy() {
        var source = new java.util.ArrayList<>(List.of("a"));
        var roster = PhaseRoster.of(source);
        source.add("b");
        assertEquals(Set.of("a"), roster.members());
        assertTrue(roster.isKnown());
    }
}
