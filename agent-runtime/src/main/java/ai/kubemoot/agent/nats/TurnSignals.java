package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.Set;

/**
 * Which signals end what, in one place for the agent and the coordinator. An agent's run ends
 * when it publishes a terminal verdict, a question (it then waits for the answer) or a declared
 * kind; the coordinator counts the agent's turn in the phase as ended only on a terminal verdict,
 * or on a declared kind the archetype marks terminal. A question never ends the phase turn.
 */
final class TurnSignals {

    /** The verdicts that end an agent's turn in a phase; {@code contribution} and {@code decline} are aliases. */
    private static final Set<String> TERMINAL_VERDICTS = Set.of(
            DiscussionSubscriber.SIGNAL_AGREE, "contribution", DiscussionSubscriber.SIGNAL_CONCERN,
            DiscussionSubscriber.SIGNAL_BLOCK, DiscussionSubscriber.SIGNAL_STAND_ASIDE, "decline",
            DiscussionSubscriber.SIGNAL_FAILURE);

    private TurnSignals() {
    }

    /** True when publishing {@code signal} ends the agent's evaluation run (the agent side). */
    static boolean endsRun(String signal) {
        return TERMINAL_VERDICTS.contains(signal) || SignalVerdicts.MSG_QUESTION.equals(signal)
                || SignalVerdicts.MSG_DECLARED.equals(signal);
    }

    /**
     * True when the message ends the agent's turn in the phase (the coordinator side): a terminal
     * verdict, or a declared kind whose metadata says the archetype marks it terminal.
     */
    static boolean endsPhaseTurn(String messageType, JsonNode msg) {
        if (SignalVerdicts.MSG_DECLARED.equals(messageType)) {
            return msg != null && msg.path("metadata").path(SignalVerdicts.FIELD_TERMINAL).asBoolean(true);
        }
        return TERMINAL_VERDICTS.contains(messageType);
    }
}
