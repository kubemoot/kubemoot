package ai.kubemoot.agent.modelserver;

import java.util.Collection;
import java.util.List;
import java.util.function.ToLongFunction;

/**
 * Whether a call's prompt fits the context window a server gives a model.
 *
 * <p>An inference engine does not reject a prompt larger than its context: Ollama
 * drops the oldest messages, keeping the system prompt and the last message, and
 * cuts the prompt itself when that is still too long. The model then answers
 * without the question, or without the evidence, and nothing reports it. So the
 * selector treats the context as a hard limit, like VRAM: a (server, model)
 * pair whose context cannot hold the prompt is never chosen.</p>
 *
 * <p>The context comes from {@link ServerState#contextLengthFor}. An unknown
 * context (zero) or an unknown prompt size (zero) never refuses a call. The check
 * covers the prompt only, not the reply: the prompt is what the engine cuts
 * without an error, and a reply that runs past the context is bounded by the
 * call's output cap and ends early instead of losing the question.</p>
 */
public final class ContextFit {

    private ContextFit() {}

    /** True when {@code promptTokens} fits the context {@code model} gets on {@code server}, or either is unknown. */
    public static boolean holds(ServerState server, String model, long promptTokens) {
        if (promptTokens <= 0 || server == null) return true;
        long context = server.contextLengthFor(model);
        return context <= 0 || promptTokens <= context;
    }

    /**
     * True when some server could run one of {@code models} with a prompt of
     * {@code promptTokens}, ignoring what is loaded or busy now. False means the
     * prompt is larger than every known context: waiting for capacity cannot help.
     */
    public static boolean anyCanHold(List<ServerState> states, Collection<String> models, long promptTokens) {
        if (states == null || states.isEmpty() || promptTokens <= 0) return true;
        for (ServerState p : states) {
            for (String m : models) {
                if (holds(p, m, promptTokens)) return true;
            }
        }
        return false;
    }

    /**
     * True when some server could ever run one of {@code models} with a prompt
     * of {@code promptTokens}: its usable VRAM holds that model's footprint (from
     * {@code footprintMiB}; zero or an unpublished VRAM total counts as fitting)
     * and its context for that model holds the prompt. VRAM and context are judged
     * for the same server and model, so a server whose context is large enough
     * only for a model it cannot hold does not count.
     */
    public static boolean anyCanRun(List<ServerState> states, Collection<String> models, long promptTokens,
                                    ToLongFunction<String> footprintMiB) {
        if (states == null || states.isEmpty() || promptTokens <= 0) return true;
        for (ServerState p : states) {
            for (String m : models) {
                if (memoryFits(p, footprintMiB.applyAsLong(m)) && holds(p, m, promptTokens)) return true;
            }
        }
        return false;
    }

    /** True when the server's memory holds {@code fp}; unknown memory or footprint fits, and so does an engine that budgets none. */
    private static boolean memoryFits(ServerState p, long fp) {
        long usable = StaticFitPredictor.usableVramMiB(p.totalVramMiB());
        return !p.engineFit().budgetsMemory() || usable <= 0 || fp <= 0 || usable >= fp;
    }

    /** The largest known context any server gives any of {@code models}; zero when none is known. */
    public static long largestContext(List<ServerState> states, Collection<String> models) {
        long largest = 0L;
        if (states == null) return largest;
        for (ServerState p : states) {
            for (String m : models) {
                largest = Math.max(largest, p.contextLengthFor(m));
            }
        }
        return largest;
    }
}
