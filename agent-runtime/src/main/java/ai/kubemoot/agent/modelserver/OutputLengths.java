package ai.kubemoot.agent.modelserver;

import java.util.ArrayDeque;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * How many tokens one agent's calls have been generating lately, per model and call
 * kind: the median of the last {@value #WINDOW} successful calls. A placement plans
 * the generation of a call with it, so a model that reasons before it answers is
 * planned with the length it has been producing, not a fixed one.
 *
 * <p>Calls differ in length by agent, model and kind, and one agent runtime serves one
 * agent, so keeping the figures per runtime keeps them per agent. Safe for use from
 * several threads.</p>
 */
final class OutputLengths {

    /** How many recent calls the median is over. */
    static final int WINDOW = 16;
    /** Fewest calls that make a figure; below it the placement plans its default length. */
    static final int MIN_CALLS = 3;

    private final Map<String, ArrayDeque<Long>> recent = new HashMap<>();

    /** Takes in one successful call that generated {@code tokens}; a call with none is ignored. */
    synchronized void add(String model, String kind, long tokens) {
        if (tokens <= 0 || model == null || kind == null) {
            return;
        }
        ArrayDeque<Long> window = recent.computeIfAbsent(key(model, kind), k -> new ArrayDeque<>());
        window.addLast(tokens);
        while (window.size() > WINDOW) {
            window.removeFirst();
        }
    }

    /**
     * Takes in a call older than every call already held, so history read after newer
     * calls were added goes behind them; ignored once the window is full.
     */
    synchronized void addOlder(String model, String kind, long tokens) {
        if (tokens <= 0 || model == null || kind == null) {
            return;
        }
        ArrayDeque<Long> window = recent.computeIfAbsent(key(model, kind), k -> new ArrayDeque<>());
        if (window.size() < WINDOW) {
            window.addFirst(tokens);
        }
    }

    /** The median generated tokens of the recent calls; 0 with fewer than {@value #MIN_CALLS}. */
    synchronized long expected(String model, String kind) {
        ArrayDeque<Long> window = recent.get(key(model, kind));
        if (window == null || window.size() < MIN_CALLS) {
            return 0L;
        }
        List<Long> sorted = window.stream().sorted().toList();
        int mid = sorted.size() / 2;
        return sorted.size() % 2 == 1 ? sorted.get(mid) : (sorted.get(mid - 1) + sorted.get(mid)) / 2;
    }

    private static String key(String model, String kind) {
        return model + "|" + kind;
    }
}
