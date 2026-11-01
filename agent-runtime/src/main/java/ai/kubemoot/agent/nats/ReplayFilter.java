package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;

import java.time.Instant;
import java.time.format.DateTimeParseException;
import java.util.Set;

/**
 * Which of the messages read back from the bus a restarted coordinator still has to
 * apply to a thread. The thread's log already holds the effect of the signals it recorded
 * and of the controls it honored; applying them again would undo the later state (an
 * {@code evaluating} replayed after the agent's {@code agree} would leave the agent
 * pending). The live subscription uses {@link #LIVE}, which applies everything.
 *
 * <p>A signal is skipped when its message id is one the log recorded. A control (stop,
 * pause, resume) has no id in the log, so it is applied only when it was sent after the
 * thread's last logged decision. A heartbeat is never replayed: it only refreshes a
 * deadline, and the deadlines are renewed when the thread is taken over.
 */
final class ReplayFilter {

    /** The live subscription: every message is applied. */
    static final ReplayFilter LIVE = new ReplayFilter(null, Set.of());

    private static final String HEARTBEAT = "heartbeat";

    private final Instant lastLoggedAt;
    private final Set<String> loggedMessageIds;

    /**
     * @param lastLoggedAt     when the thread's last event was written; null for the live filter
     * @param loggedMessageIds the bus messages whose signals the log recorded
     */
    ReplayFilter(Instant lastLoggedAt, Set<String> loggedMessageIds) {
        this.lastLoggedAt = lastLoggedAt;
        this.loggedMessageIds = loggedMessageIds;
    }

    /** True when the signal in {@code msg} (of {@code messageType}) is not yet in the thread's state. */
    boolean appliesSignal(String messageType, JsonNode msg) {
        if (lastLoggedAt == null) {
            return true;
        }
        if (HEARTBEAT.equals(messageType)) {
            return false;
        }
        boolean recorded = CoordinatorWire.signalFor(messageType) != null || CoordinatorWire.FAILURE.equals(messageType);
        String messageId = JsonFields.text(msg, CoordinatorWire.MESSAGE_ID);
        return !(recorded && !messageId.isEmpty() && loggedMessageIds.contains(messageId));
    }

    /** True when the control in {@code msg} was sent after the thread's last logged decision. */
    boolean appliesControl(JsonNode msg) {
        if (lastLoggedAt == null) {
            return true;
        }
        try {
            return Instant.parse(JsonFields.text(msg, "timestamp")).isAfter(lastLoggedAt);
        } catch (DateTimeParseException e) {
            return false;
        }
    }
}
