package ai.kubemoot.agent.provider;

/**
 * Asks the operator to make a model ready on a provider, or to release it. The
 * operator's engine adapter does the waking and sleeping; the runtime only asks.
 */
public interface ModelLifecycle {

    /**
     * The operator's answer: whether the provider reports the model in the state asked
     * for, the state, how long making it ready took, and the reason for a failure.
     */
    record Reply(boolean ok, String state, long switchMs, String error) {
        static Reply failure(String error) {
            return new Reply(false, "", 0L, error);
        }
    }

    /** Makes {@code model} ready on {@code provider}; returns when the provider reports it awake. */
    Reply ready(String provider, String model);

    /** Releases {@code model} on {@code provider}; returns when the provider reports it asleep. */
    Reply release(String provider, String model);
}
