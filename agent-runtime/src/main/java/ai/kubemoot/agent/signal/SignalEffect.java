package ai.kubemoot.agent.signal;

import java.util.Map;

/**
 * What the runtime does with a declared kind of signal: the one table from an archetype's names
 * to the runtime's phase logic. The archetype owns the names and their meanings; this table
 * names the kinds the runtime has logic for. A declared kind outside it is {@link #DECLARED}:
 * it is published and recorded under its own name, and settles the agent's turn when the
 * archetype says it is terminal.
 */
public enum SignalEffect {
    /** The finding stands; published as {@code agree}. */
    AGREE,
    /** A finding is wrong or unverified; published as {@code concern}. */
    CONCERN,
    /** A clarifying question to the agent that made a finding; published as {@code question}. */
    QUESTION,
    /** Nothing to add; published as {@code stand_aside}. */
    STAND_ASIDE,
    /** A tool the answer needs is missing; published as a {@code concern} whose content is the need. */
    TOOL_GAP,
    /** The tools ran and returned nothing usable; published as a {@code failure}. */
    NO_DATA,
    /** A finding or the answer contradicts the agent's results; published as {@code block}. */
    BLOCK,
    /** A kind the runtime has no phase logic for; published as {@code signal} with its name. */
    DECLARED;

    private static final Map<String, SignalEffect> BY_NAME = Map.of(
            "agree", AGREE,
            "concern", CONCERN,
            "question", QUESTION,
            "stand_aside", STAND_ASIDE,
            "tool_gap", TOOL_GAP,
            "no_data", NO_DATA,
            "block", BLOCK);

    /** The effect of the kind named {@code name}; {@link #DECLARED} for a name outside the table. */
    public static SignalEffect of(String name) {
        return name == null ? DECLARED : BY_NAME.getOrDefault(name, DECLARED);
    }

    /** True for a kind that says the agent has no finding to stand on, so no computation is owed. */
    public boolean withoutFinding() {
        return this == NO_DATA || this == TOOL_GAP || this == STAND_ASIDE;
    }
}
