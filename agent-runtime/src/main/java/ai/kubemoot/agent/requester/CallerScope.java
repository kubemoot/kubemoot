package ai.kubemoot.agent.requester;

import java.util.Optional;

/**
 * The caller of the model turn running on this thread, bound the way {@link RequesterScope}
 * binds the requester: around a turn's tool loop, and read when a tool is called. A binding lasts
 * exactly until its scope is closed and is visible only to the thread that made it, so two turns
 * running at once never see each other's caller.
 */
public final class CallerScope {

    private static final ThreadLocal<Caller> CURRENT = new ThreadLocal<>();

    private CallerScope() {
    }

    /** Binds the caller (null for none) until the returned scope is closed, then restores the previous one. */
    public static RequesterScope.Scope bind(Caller caller) {
        Caller previous = CURRENT.get();
        set(caller);
        return () -> set(previous);
    }

    public static Optional<Caller> current() {
        return Optional.ofNullable(CURRENT.get());
    }

    private static void set(Caller caller) {
        if (caller == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(caller);
        }
    }
}
