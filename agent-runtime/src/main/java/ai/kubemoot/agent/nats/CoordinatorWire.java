package ai.kubemoot.agent.nats;

import java.util.Map;
import java.util.Set;

/**
 * The words of the coordinator log: the field names inside an event's {@code data} and
 * the names a signal is recorded under. The recorder writes them and the reducer reads
 * them, so they are defined once, here. The JSON Schema ({@code coordinator-event.json})
 * is the published form of the same names.
 */
final class CoordinatorWire {

    // --- fields of data ---
    static final String AGENT = "agent";
    static final String PHASE = "phase";
    static final String SIGNAL = "signal";
    static final String MESSAGE_ID = "messageId";
    static final String LAST_SIGNAL_AT = "lastSignalAt";
    static final String PHASE_STARTED_AT = "phaseStartedAt";
    static final String ADVISORY_PENDING = "advisoryPending";
    static final String ROSTER = "roster";
    static final String PENDING = "pending";
    static final String DEADLINE_AT = "deadlineAt";
    static final String RESEARCHER = "researcher";
    static final String CAPACITY_REASON = "capacityReason";
    static final String TOO_LARGE_MODEL = "tooLargeModel";
    static final String MODEL = "model";
    static final String SYNTHESIZED = "synthesized";
    static final String FAILED_SOURCES = "failedSources";
    static final String CAUSE = "cause";
    static final String REASON = "reason";
    static final String CHANNEL = "channel";

    // --- signal names ---
    static final String AGREE = "agree";
    static final String CONCERN = "concern";
    static final String BLOCK = "block";
    static final String STAND_ASIDE = "stand_aside";
    static final String TRIAGING = "triaging";
    static final String EVALUATING = "evaluating";
    static final String WAITING = "waiting";
    static final String WAKING = "waking";
    static final String READY = "ready";
    static final String FAILURE = "failure";

    /**
     * The name a message type is recorded under: the {@code contribution} and
     * {@code decline} aliases fold into {@code agree} and {@code stand_aside}. A message
     * type absent here (a heartbeat, an advisory) is not recorded.
     */
    private static final Map<String, String> RECORDED = Map.of(
            TRIAGING, TRIAGING, EVALUATING, EVALUATING, WAITING, WAITING,
            AGREE, AGREE, "contribution", AGREE,
            CONCERN, CONCERN, BLOCK, BLOCK,
            STAND_ASIDE, STAND_ASIDE, "decline", STAND_ASIDE);

    /** The recorded signals that end an agent's turn (a failure does too, as its own event). */
    private static final Set<String> TERMINAL = Set.of(AGREE, CONCERN, BLOCK, STAND_ASIDE);

    private CoordinatorWire() {
    }

    /** The recorded name of {@code messageType}, or null when it is not a signal the log records. */
    static String signalFor(String messageType) {
        return RECORDED.get(messageType);
    }

    /** True for a recorded signal that ends an agent's turn. */
    static boolean isTerminal(String signal) {
        return TERMINAL.contains(signal);
    }
}
