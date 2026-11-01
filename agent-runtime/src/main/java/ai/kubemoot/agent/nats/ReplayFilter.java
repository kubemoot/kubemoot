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
 * pause, resume) has no id in the log, so it is applied only when it came after the
 * thread's last logged decision: by stream sequence when the log states one, else by time. A heartbeat is never replayed: it only refreshes a
 * deadline, and the deadlines are renewed when the thread is taken over.
 */
final class ReplayFilter {

    /** The live subscription: every message is applied. */
    static final ReplayFilter LIVE = new ReplayFilter(null, Set.of(), 0);

    private static final String HEARTBEAT = "heartbeat";

    private final Instant lastLoggedAt;
    private final Set<String> loggedMessageIds;
    private final long loggedDiscussSeq;

    /** A filter that orders controls by time, for a log whose events carry no stream sequence. */
    ReplayFilter(Instant lastLoggedAt, Set<String> loggedMessageIds) {
        this(lastLoggedAt, loggedMessageIds, 0);
    }

    /**
     * @param lastLoggedAt     when the thread's last event was written; null for the live filter
     * @param loggedMessageIds the bus messages whose signals the log recorded
     * @param loggedDiscussSeq the highest discussion stream sequence the log's events state they had
     *                         applied; when above 0 a control is ordered by its stream sequence
     */
    ReplayFilter(Instant lastLoggedAt, Set<String> loggedMessageIds, long loggedDiscussSeq) {
        this.lastLoggedAt = lastLoggedAt;
        this.loggedMessageIds = loggedMessageIds;
        this.loggedDiscussSeq = loggedDiscussSeq;
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

    /** {@link #appliesControl(JsonNode, long)} for a message whose stream sequence is not known. */
    boolean appliesControl(JsonNode msg) {
        return appliesControl(msg, 0);
    }

    /**
     * True when the control in {@code msg}, which sits at {@code streamSequence} of the discussion
     * stream (0 when unknown), came after the thread's last logged decision. When the log states
     * the stream sequence it had applied, the two sequences are compared and no clock is read, so
     * clock skew between the sender and the coordinator cannot drop a fresh control or apply a
     * stale one. Otherwise the message's own timestamp is compared with the time of the last event.
     */
    boolean appliesControl(JsonNode msg, long streamSequence) {
        if (lastLoggedAt == null) {
            return true;
        }
        if (loggedDiscussSeq > 0 && streamSequence > 0) {
            return streamSequence > loggedDiscussSeq;
        }
        try {
            return Instant.parse(JsonFields.text(msg, "timestamp")).isAfter(lastLoggedAt);
        } catch (DateTimeParseException e) {
            return false;
        }
    }
}
