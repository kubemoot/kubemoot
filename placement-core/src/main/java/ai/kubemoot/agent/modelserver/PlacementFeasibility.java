package ai.kubemoot.agent.modelserver;

import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.function.Function;
import java.util.function.ToLongFunction;

/**
 * Why a call that found no room now will never be placed, as opposed to a busy cluster where
 * waiting is right. Three refusals exist: no accelerator can ever hold the model, no ready server
 * has the model downloaded, and no server's context window holds the prompt. The agent stands
 * aside on the first and third and fails the call on the second; none of them is cured by waiting.
 *
 * <p>Both the in-process caller and the placement service ask this one place, so a refusal reads
 * the same whichever side decides.</p>
 */
public final class PlacementFeasibility {

    /** The model is larger than any accelerator's usable memory. */
    public static final String MODEL_TOO_LARGE = NoFitException.REASON_MODEL_TOO_LARGE;
    /** No ready server has the model or an acceptable alternative downloaded. */
    public static final String NO_SERVER_HAS_MODEL = "no-server-has-model";
    /** The prompt exceeds every server's context window for the acceptable models. */
    public static final String PROMPT_TOO_LARGE = NoFitException.REASON_PROMPT_TOO_LARGE;

    /** A refusal: its {@code code} is one of the constants above, {@code detail} says why. */
    public record Refusal(String code, String model, String detail) {

        /** The exception the caller raises for this refusal. */
        public RuntimeException toException() {
            return switch (code) {
                case MODEL_TOO_LARGE -> NoFitException.modelTooLarge(model, detail);
                case PROMPT_TOO_LARGE -> NoFitException.promptTooLarge(model, detail);
                default -> new IllegalStateException(detail);
            };
        }
    }

    private PlacementFeasibility() {}

    /**
     * The refusal for a call to {@code model} that no server has room for now, or empty when waiting
     * for capacity can still place it.
     *
     * @param models           the acceptable models, preferred first
     * @param promptTokens     the prompt's estimated size
     * @param footprintMiB     the preferred model's cold-load footprint
     * @param footprint        each acceptable model's cold-load footprint
     * @param activeModelsOn   the models held by in-flight claims on a server, which count as present there
     */
    public static Optional<Refusal> refusal(List<ServerState> states, String model, long footprintMiB,
                                            List<String> models, long promptTokens,
                                            ToLongFunction<String> footprint,
                                            Function<String, Set<String>> activeModelsOn) {
        if (!ServerSelector.canEverHold(states, model, footprintMiB)) {
            return Optional.of(new Refusal(MODEL_TOO_LARGE, model,
                    "model=" + model + " coldLoad=" + footprintMiB + "MiB exceeds every GPU's usable VRAM"));
        }
        if (!ServerSelector.someServerHasAny(states, models, activeModelsOn)) {
            return Optional.of(new Refusal(NO_SERVER_HAS_MODEL, model,
                    "no ready model server has " + String.join(" or ", models) + " downloaded"));
        }
        if (!ContextFit.anyCanRun(states, models, promptTokens, footprint)) {
            long largest = ContextFit.largestContext(states, models);
            return Optional.of(new Refusal(PROMPT_TOO_LARGE, model,
                    "prompt ~" + promptTokens + " tokens exceeds the largest context window (" + largest
                            + " tokens) any server gives " + models));
        }
        return Optional.empty();
    }
}
