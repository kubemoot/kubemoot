package ai.kubemoot.agent.nats;

import java.util.Collection;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * The participants a discussion phase waits for, and which of them have
 * published a terminal signal in this phase. EVALUATING waits for the selected
 * toolers; REVIEW waits for the analysts review_ready woke.
 *
 * <p>A thread swaps in a new roster when a phase begins, so a signal from an
 * earlier phase never counts for the next one. A roster with no members is
 * unknown: the phase cannot settle on state and falls back to its floor.
 */
final class PhaseRoster {

    private final Set<String> members;
    private final Set<String> signalled = ConcurrentHashMap.newKeySet();

    private PhaseRoster(Set<String> members) {
        this.members = members;
    }

    /** A roster of {@code members}, none of which has signalled yet. */
    static PhaseRoster of(Collection<String> members) {
        return new PhaseRoster(members == null ? Set.of() : Set.copyOf(members));
    }

    /** The roster of a phase whose participants are not known. */
    static PhaseRoster unknown() {
        return new PhaseRoster(Set.of());
    }

    Set<String> members() {
        return members;
    }

    boolean isKnown() {
        return !members.isEmpty();
    }

    /**
     * Record that {@code agent} published a terminal signal (agree, concern,
     * block, stand_aside, or failure, including a failure the coordinator
     * synthesizes when the agent's deadline expires). Non-members are ignored.
     */
    void recordTerminalSignal(String agent) {
        if (agent != null && members.contains(agent)) {
            signalled.add(agent);
        }
    }

    /**
     * The settle guard: true only when the roster is known, every member has
     * published a terminal signal in this phase, and none is still pending. A
     * member that has only published triaging, evaluating, a heartbeat, or a
     * waiting signal holds the phase.
     */
    boolean allSignalled(Map<String, Long> pending) {
        if (members.isEmpty()) {
            return false;
        }
        for (String member : members) {
            if (!signalled.contains(member) || pending.containsKey(member)) {
                return false;
            }
        }
        return true;
    }
}
