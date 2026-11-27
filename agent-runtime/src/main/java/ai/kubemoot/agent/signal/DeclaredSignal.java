package ai.kubemoot.agent.signal;

/**
 * One kind of verdict the crew's archetype declares: its name (the {@code kind} an agent passes
 * to the signal tool), the meaning the model is shown, whether it ends the agent's turn in the
 * phase, and whether it is said to one named agent.
 */
public record DeclaredSignal(String name, String meaning, boolean terminal, boolean addressed) {

    /** What the runtime does with this kind in a phase, from its name (see {@link SignalEffect}). */
    public SignalEffect effect() {
        return SignalEffect.of(name);
    }
}
