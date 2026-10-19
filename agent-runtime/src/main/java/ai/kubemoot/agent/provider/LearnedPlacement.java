package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.Placement;
import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Locale;
import java.util.Optional;

/**
 * The learned placement policy: places each call where it is predicted to finish
 * soonest, from what the providers' models were measured to cost (calibration, and
 * the observed speed of crew calls since; {@link ModelSpeed}).
 *
 * <p>Predicted completion of an option is the wait for a slot, plus the load time
 * when the model is cold there, plus the prompt over the prompt speed, plus the
 * expected output over the generation speed. A fast accelerator therefore wins by
 * default, and a slower provider (a CPU) only when the fast ones are busy enough
 * that it finishes first; no rule names a call type or a provider.</p>
 *
 * <p>It keeps the crew's model choice: the preferred model is placed on its soonest
 * provider, and an alternative is considered only when the preferred model has no
 * option at all, so this policy changes where calls run, not which model answers.
 * It has no opinion, and the fallback places the call, when any option of the model
 * is unmeasured (an unmeasured provider is never guessed fast or slow), when its
 * soonest option has to wait for a slot (the fallback weighs queueing against
 * unloading idle models), or when another call takes the chosen slot or load first.
 * A cold load yields to a load of the same model already in progress anywhere, so
 * concurrent calls still converge on one copy.</p>
 *
 * <p>The wait for a slot comes from the provider's recent call time, which reflects
 * real output lengths; the generation term plans a fixed output length
 * ({@link #EXPECTED_OUTPUT_TOKENS}) for every option, so options compare by speed.</p>
 */
public class LearnedPlacement implements PlacementPolicy {

    private static final Logger log = LoggerFactory.getLogger(LearnedPlacement.class);

    /**
     * Output tokens a call is planned to generate. Every option of a call is planned
     * with the same length, so it scales the generation term evenly and the ranking
     * follows each provider's generation speed.
     */
    static final long EXPECTED_OUTPUT_TOKENS = 256;

    /** Where the learned policy would run a call, and why. Proposing claims nothing. */
    public record Proposal(String model, String provider, String reason) {}

    /** An option with its prediction. */
    record Ranked(String model, ProviderSelector.Option option, Prediction prediction) {}

    /** The predicted seconds to finish, by part. */
    record Prediction(double queue, double load, double prompt, double generate) {
        double total() {
            return queue + load + prompt + generate;
        }

        String explain() {
            return String.format(Locale.ROOT, "predicted %.1fs (wait %.1f, load %.1f, prompt %.1f, generate %.1f)",
                    total(), queue, load, prompt, generate);
        }
    }

    private final PlacementPolicy fallback;
    private final ProviderSelector selector;

    /**
     * {@code fallback} places every call this policy has no opinion on or cannot
     * claim; {@code selector} sees and claims providers. A null selector gives a
     * policy with no opinion.
     */
    public LearnedPlacement(PlacementPolicy fallback, ProviderSelector selector) {
        this.fallback = fallback;
        this.selector = selector;
    }

    /** A policy with no opinion of its own: every call goes to {@code fallback}. */
    public LearnedPlacement(PlacementPolicy fallback) {
        this(fallback, null);
    }

    @Override
    public PlacementMode mode() {
        return PlacementMode.LEARNED;
    }

    /** Where this policy would place the call, without claiming anything; empty with no opinion. */
    public Optional<Proposal> propose(PlacementRequest request) {
        return best(request).map(r -> new Proposal(r.model(), r.option().provider().name(), r.prediction().explain()));
    }

    @Override
    public Optional<Placement> place(PlacementRequest request) {
        Optional<Ranked> best = best(request).filter(r -> r.option().startsNow());
        if (best.isEmpty()) {
            return fallback.place(request);
        }
        Ranked r = best.get();
        long fp = request.footprint().applyAsLong(r.model());
        Optional<Pick> pick = selector.pickAndClaimOn(r.option().provider().name(), startOf(r.option()), r.model(),
                fp, request.kv().applyAsLong(r.model()), request.promptTokens());
        if (pick.isEmpty()) {
            log.debug("Learned choice {} on {} was taken; the fallback places the call", r.model(),
                    r.option().provider().name());
            return fallback.place(request);
        }
        log.info("Learned placement: {} on {}, {}", r.model(), r.option().provider().name(), r.prediction().explain());
        return Optional.of(new Placement(pick.get(), r.model(), fp, List.of()));
    }

    /** How the call starts on an option it can start on now. */
    static ProviderSelector.Start startOf(ProviderSelector.Option o) {
        return o.warm() ? ProviderSelector.Start.SLOT : ProviderSelector.Start.LOAD;
    }

    /**
     * The soonest-finishing option of the first model, in the request's order, that
     * has any option: empty when that model has an unmeasured option, when none of
     * its options is on a provider that has it, or when no model has an option.
     */
    Optional<Ranked> best(PlacementRequest request) {
        if (selector == null) {
            return Optional.empty();
        }
        for (String model : request.acceptable()) {
            long fp = request.footprint().applyAsLong(model);
            List<ProviderSelector.Option> options = fp <= 0 ? List.of()
                    : selector.options(model, fp, request.kv().applyAsLong(model), request.promptTokens());
            if (!options.isEmpty()) {
                return soonest(model, options, request.promptTokens());
            }
        }
        return Optional.empty();
    }

    /**
     * The soonest of the options on providers that have the model (loaded or
     * downloaded); empty when any of those is unmeasured. A provider without the
     * model cannot serve the call, so it is neither an option nor a reason for no
     * opinion.
     */
    static Optional<Ranked> soonest(String model, List<ProviderSelector.Option> options, long promptTokens) {
        Ranked best = null;
        for (ProviderSelector.Option o : options) {
            if (!o.provider().hasModel(model)) {
                continue;
            }
            Optional<Prediction> p = predict(o, model, promptTokens);
            if (p.isEmpty()) {
                return Optional.empty();
            }
            if (best == null || p.get().total() < best.prediction().total()) {
                best = new Ranked(model, o, p.get());
            }
        }
        return Optional.ofNullable(best);
    }

    /**
     * The predicted completion of {@code model} on the option; empty when the
     * provider's speeds for it are unknown, so an unmeasured provider is never
     * guessed fast.
     */
    static Optional<Prediction> predict(ProviderSelector.Option o, String model, long promptTokens) {
        Optional<ModelSpeed> speed = o.provider().speedOf(model);
        if (speed.isEmpty() || speed.get().generateSpeed() <= 0 || speed.get().promptTokensPerSecond() <= 0) {
            return Optional.empty();
        }
        ModelSpeed s = speed.get();
        double load = o.warm() ? 0.0 : o.provider().switchSeconds(model).orElse(s.loadMs() / 1000.0);
        double prompt = Math.max(promptTokens, 0L) / (double) s.promptTokensPerSecond();
        double generate = EXPECTED_OUTPUT_TOKENS / (double) s.generateSpeed();
        return Optional.of(new Prediction(Math.max(o.waitSeconds(), 0.0), load, prompt, generate));
    }
}
