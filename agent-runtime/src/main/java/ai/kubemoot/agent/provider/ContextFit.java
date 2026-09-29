package ai.kubemoot.agent.provider;

import java.util.Collection;
import java.util.List;
import java.util.function.ToLongFunction;

/**
 * Whether a call's prompt fits the context window a provider gives a model.
 *
 * <p>An inference engine does not reject a prompt larger than its context: Ollama
 * drops the oldest messages, keeping the system prompt and the last message, and
 * cuts the prompt itself when that is still too long. The model then answers
 * without the question, or without the evidence, and nothing reports it. So the
 * selector treats the context as a hard limit, like VRAM: a (provider, model)
 * pair whose context cannot hold the prompt is never chosen.</p>
 *
 * <p>The context comes from {@link ProviderState#contextLengthFor}. An unknown
 * context (zero) or an unknown prompt size (zero) never refuses a call. The check
 * covers the prompt only, not the reply: the prompt is what the engine cuts
 * without an error, and a reply that runs past the context is bounded by the
 * call's output cap and ends early instead of losing the question.</p>
 */
public final class ContextFit {

    private ContextFit() {}

    /** True when {@code promptTokens} fits the context {@code model} gets on {@code provider}, or either is unknown. */
    public static boolean holds(ProviderState provider, String model, long promptTokens) {
        if (promptTokens <= 0 || provider == null) return true;
        long context = provider.contextLengthFor(model);
        return context <= 0 || promptTokens <= context;
    }

    /**
     * True when some provider could run one of {@code models} with a prompt of
     * {@code promptTokens}, ignoring what is loaded or busy now. False means the
     * prompt is larger than every known context: waiting for capacity cannot help.
     */
    public static boolean anyCanHold(List<ProviderState> states, Collection<String> models, long promptTokens) {
        if (states == null || states.isEmpty() || promptTokens <= 0) return true;
        for (ProviderState p : states) {
            for (String m : models) {
                if (holds(p, m, promptTokens)) return true;
            }
        }
        return false;
    }

    /**
     * True when some provider could ever run one of {@code models} with a prompt
     * of {@code promptTokens}: its usable VRAM holds that model's footprint (from
     * {@code footprintMiB}; zero or an unpublished VRAM total counts as fitting)
     * and its context for that model holds the prompt. VRAM and context are judged
     * for the same provider and model, so a provider whose context is large enough
     * only for a model it cannot hold does not count.
     */
    public static boolean anyCanRun(List<ProviderState> states, Collection<String> models, long promptTokens,
                                    ToLongFunction<String> footprintMiB) {
        if (states == null || states.isEmpty() || promptTokens <= 0) return true;
        for (ProviderState p : states) {
            long usable = StaticFitPredictor.usableVramMiB(p.totalVramMiB());
            for (String m : models) {
                long fp = footprintMiB.applyAsLong(m);
                boolean fits = usable <= 0 || fp <= 0 || usable >= fp;
                if (fits && holds(p, m, promptTokens)) return true;
            }
        }
        return false;
    }

    /** The largest known context any provider gives any of {@code models}; zero when none is known. */
    public static long largestContext(List<ProviderState> states, Collection<String> models) {
        long largest = 0L;
        if (states == null) return largest;
        for (ProviderState p : states) {
            for (String m : models) {
                largest = Math.max(largest, p.contextLengthFor(m));
            }
        }
        return largest;
    }
}
