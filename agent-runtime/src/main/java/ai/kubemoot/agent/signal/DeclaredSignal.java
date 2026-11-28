package ai.kubemoot.agent.signal;

/**
 * One kind of verdict the crew's archetype declares: its name (the {@code kind} an agent passes
 * to the signal tool), the meaning the model is shown, whether it ends the agent's turn in the
 * phase, whether it is said to one named agent, and whether, said to one, it starts that agent's
 * recheck (a clarifying question the named agent answers before the saying agent signals again).
 */
public record DeclaredSignal(String name, String meaning, boolean terminal, boolean addressed,
                             boolean startsRecheck) {

    /** A kind that does not start a recheck. */
    public DeclaredSignal(String name, String meaning, boolean terminal, boolean addressed) {
        this(name, meaning, terminal, addressed, false);
    }

    /** What the runtime does with this kind in a phase, from its name (see {@link SignalEffect}). */
    public SignalEffect effect() {
        return SignalEffect.of(name);
    }
}
