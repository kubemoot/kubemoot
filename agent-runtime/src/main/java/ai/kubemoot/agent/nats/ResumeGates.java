package ai.kubemoot.agent.nats;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Queue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Consumer;

/**
 * One gate per thread being resumed. While a thread's gate is shut, the live messages for
 * it queue instead of being applied: the messages the coordinator missed while it was down
 * are applied first, in the order the stream holds them, and the queued live ones follow in
 * the order they arrived. Without the gate an {@code evaluating} read back from the stream
 * could land after the same agent's live {@code agree} and leave the agent pending.
 */
final class ResumeGates {

    /** A live message held back, with the discussion stream sequence it arrived at (0 when the feed gave none). */
    record Held(String data, long streamSequence) {
    }

    private static final class Gate {
        final Queue<Held> queue = new ArrayDeque<>();
        boolean open;
    }

    private final Map<String, Gate> gates = new ConcurrentHashMap<>();

    /** Shuts a gate for {@code threadId}; live messages for it queue from now on. */
    void shut(String threadId) {
        gates.put(threadId, new Gate());
    }

    /** Queues {@code data} and returns true when the thread's gate is shut; false when there is no gate or it is open. */
    boolean hold(String threadId, String data) {
        return hold(threadId, data, 0);
    }

    /** {@link #hold(String, String)} for a message at {@code streamSequence} of the discussion stream. */
    boolean hold(String threadId, String data, long streamSequence) {
        Gate gate = gates.get(threadId);
        if (gate == null) {
            return false;
        }
        synchronized (gate) {
            if (gate.open) {
                return false;
            }
            gate.queue.add(new Held(data, streamSequence));
            return true;
        }
    }

    /**
     * Opens the gate: hands the queued messages to {@code apply} in order, and keeps doing
     * so for any that queue while it runs, until none are left; only then do live messages
     * pass straight through. The order of arrival is kept.
     */
    void open(String threadId, Consumer<String> apply) {
        openHeld(threadId, held -> apply.accept(held.data()));
    }

    /** {@link #open(String, Consumer)} handing over each held message with its stream sequence. */
    void openHeld(String threadId, Consumer<Held> apply) {
        Gate gate = gates.get(threadId);
        if (gate == null) {
            return;
        }
        while (true) {
            List<Held> batch;
            synchronized (gate) {
                if (gate.queue.isEmpty()) {
                    gate.open = true;
                    break;
                }
                batch = new ArrayList<>(gate.queue);
                gate.queue.clear();
            }
            batch.forEach(apply);
        }
        gates.remove(threadId);
    }

    /** True while a gate for {@code threadId} is shut. */
    boolean isShut(String threadId) {
        Gate gate = gates.get(threadId);
        return gate != null && !gate.open;
    }
}
