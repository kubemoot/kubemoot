package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.CallPlanner.Placement;
import ai.kubemoot.agent.modelserver.CallPlanner.PlacementRequest;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Survey;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Surveyed;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Locale;
import java.util.Optional;

/**
 * The learned placement policy: places each call where it is predicted to finish
 * soonest, from what the servers' models were measured to cost (calibration, and
 * the observed speed of crew calls since; {@link ModelSpeed}).
 *
 * <p>Predicted completion of an option is the wait for a slot, plus the load time
 * when the model is cold there, plus the prompt over the prompt speed, plus the
 * expected output over the generation speed. A fast accelerator therefore wins by
 * default, and a slower server (a CPU) only when the fast ones are busy enough
 * that it finishes first; no rule names a call type or a server.</p>
 *
 * <p>It keeps the crew's model choice: the preferred model is placed on its soonest
 * server, and an alternative is considered only when the preferred model has no
 * option at all, so this policy changes where calls run, not which model answers.
 * It has no opinion, and the fallback places the call, when any option of the model
 * is unmeasured (an unmeasured server is never guessed fast or slow), when its
 * soonest option has to wait for a slot (the fallback weighs queueing against
 * unloading idle models), or when another call takes the chosen slot or load first.
 * A cold load yields to a load of the same model already in progress anywhere, so
 * concurrent calls still converge on one copy.</p>
 *
 * <p>The wait for a slot comes from the server's recent call time, which reflects
 * real output lengths; the generation term plans a fixed output length
 * ({@link #EXPECTED_OUTPUT_TOKENS}) for every option, so options compare by speed.</p>
 */
public class LearnedPlacement implements PlacementPolicy {

    private static final Logger log = LoggerFactory.getLogger(LearnedPlacement.class);

    /**
     * Output tokens a call is planned to generate when nothing is known of what the
     * agent's calls generate. Every option of a call is planned with the same length,
     * so it scales the generation term evenly and the ranking follows each server's
     * generation speed.
     */
    static final long EXPECTED_OUTPUT_TOKENS = 256;

    /** Where the learned policy would run a call, and why. Proposing claims nothing. */
    public record Proposal(String model, String server, String reason) {}

    /** An option with its prediction. */
    record Ranked(String model, ServerSelector.Option option, Prediction prediction) {}

    /** The predicted seconds to finish, by part. */
    public record Prediction(double queue, double load, double prompt, double generate) {
        /** Seconds to finish: every part. */
        public double total() {
            return queue + load + prompt + generate;
        }

        String explain() {
            return String.format(Locale.ROOT, "predicted %.1fs (wait %.1f, load %.1f, prompt %.1f, generate %.1f)",
                    total(), queue, load, prompt, generate);
        }
    }

    private final PlacementPolicy fallback;
    private final ServerSelector selector;

    /**
     * {@code fallback} places every call this policy has no opinion on or cannot
     * claim; {@code selector} sees and claims servers. A null selector gives a
     * policy with no opinion.
     */
    public LearnedPlacement(PlacementPolicy fallback, ServerSelector selector) {
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
        return best(request).map(r -> new Proposal(r.model(), r.option().server().name(), r.prediction().explain()));
    }

    @Override
    public Optional<Placement> place(PlacementRequest request) {
        Optional<Survey> survey = PlacementSurvey.of(selector, request);
        Optional<Surveyed> best = survey.flatMap(Survey::soonest).filter(s -> s.option().startsNow());
        if (best.isEmpty()) {
            return placeByFallback(request, survey);
        }
        Surveyed r = best.get();
        long fp = request.footprint().applyAsLong(r.model());
        Optional<Pick> pick = selector.pickAndClaimOn(r.server(), startOf(r.option()), r.model(),
                fp, request.kv().applyAsLong(r.model()), request.promptTokens());
        if (pick.isEmpty()) {
            log.debug("Learned choice {} on {} was taken; the fallback places the call", r.model(), r.server());
            return placeByFallback(request, survey);
        }
        Prediction prediction = r.prediction().orElseThrow();
        log.info("Learned placement: {} on {}, {}", r.model(), r.server(), prediction.explain());
        return Optional.of(new Placement(pick.get(), r.model(), fp, List.of(),
                PlacementDecisions.learned(r, survey.orElseThrow())));
    }

    /** The fallback's placement, explained with what this policy thought of the call. */
    private Optional<Placement> placeByFallback(PlacementRequest request, Optional<Survey> survey) {
        return fallback.place(request).map(p -> PlacementDecisions.explained(p, survey, PlacementMode.LEARNED));
    }

    /** How the call starts on an option it can start on now. */
    static ServerSelector.Start startOf(ServerSelector.Option o) {
        return o.warm() ? ServerSelector.Start.SLOT : ServerSelector.Start.LOAD;
    }

    /**
     * The soonest-finishing option of the first model, in the request's order, that
     * has any option: empty when that model has an unmeasured option, when none of
     * its options is on a server that has it, or when no model has an option.
     */
    Optional<Ranked> best(PlacementRequest request) {
        return PlacementSurvey.of(selector, request).flatMap(Survey::soonest)
                .map(s -> new Ranked(s.model(), s.option(), s.prediction().orElseThrow()));
    }

    /**
     * The predicted completion of {@code model} on the option; empty when the
     * server's speeds for it are unknown, so an unmeasured server is never
     * guessed fast.
     */
    static Optional<Prediction> predict(ServerSelector.Option o, String model, long promptTokens) {
        return predict(o, model, promptTokens, 0L);
    }

    /**
     * {@link #predict(ServerSelector.Option, String, long)} for a call expected to
     * generate {@code outputTokens} (0 plans {@link #EXPECTED_OUTPUT_TOKENS}). On a
     * server with a free slot, other calls sharing it slow this one down: the model's
     * load factor for the calls it would run among stretches the prompt and the
     * generation, and the stretch is counted in the wait. A server without a free
     * slot makes the call wait instead, which the wait already covers.
     */
    static Optional<Prediction> predict(ServerSelector.Option o, String model, long promptTokens, long outputTokens) {
        Optional<ModelSpeed> speed = o.server().speedOf(model);
        if (speed.isEmpty() || speed.get().generateSpeed() <= 0 || speed.get().promptTokensPerSecond() <= 0) {
            return Optional.empty();
        }
        ModelSpeed s = speed.get();
        double load = o.warm() ? 0.0 : o.server().switchSeconds(model).orElse(s.loadMs() / 1000.0);
        double prompt = Math.max(promptTokens, 0L) / (double) s.promptTokensPerSecond();
        double generate = (outputTokens > 0 ? outputTokens : EXPECTED_OUTPUT_TOKENS) / (double) s.generateSpeed();
        double sharing = o.freeSlot() ? (prompt + generate) * (s.slowdown(o.activeCalls() + 1L) - 1.0) : 0.0;
        return Optional.of(new Prediction(Math.max(o.waitSeconds(), 0.0) + sharing, load, prompt, generate));
    }
}
