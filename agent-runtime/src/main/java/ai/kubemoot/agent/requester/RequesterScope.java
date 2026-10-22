package ai.kubemoot.agent.requester;

import java.util.Optional;

/**
 * The requester of the model turn running on this thread. The chat service binds it around a
 * turn's tool loop; the tool client reads it when a tool is called, so every tool call of the
 * turn carries the requester without each tool executor taking it as a parameter.
 */
public final class RequesterScope {

    private static final ThreadLocal<Requester> CURRENT = new ThreadLocal<>();

    private RequesterScope() {
    }

    /** Binds the requester (null for none) until the returned scope is closed, then restores the previous one. */
    public static Scope bind(Requester requester) {
        Requester previous = CURRENT.get();
        set(requester);
        return () -> set(previous);
    }

    public static Optional<Requester> current() {
        return Optional.ofNullable(CURRENT.get());
    }

    private static void set(Requester requester) {
        if (requester == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(requester);
        }
    }

    /** Closing it ends the binding; it throws nothing. */
    @FunctionalInterface
    public interface Scope extends AutoCloseable {
        @Override
        void close();
    }
}
