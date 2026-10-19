package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.util.Optional;
import java.util.function.ToLongFunction;

/**
 * Which other models a mulling call may use instead of the agent's bound model.
 *
 * <p>Reads the operator-published candidate list
 * ({@code KUBEMOOT_MODEL_CANDIDATES_MULLING}) and the quality tolerance
 * ({@code KUBEMOOT_MODEL_CANDIDATE_TOLERANCE}, default
 * {@value #DEFAULT_TOLERANCE} score points). A candidate qualifies when its
 * quality score is at most the tolerance below the bound model's score. With the
 * {@code qualityBias} scoring over the model's size class, one size tier is
 * {@code |bias - 0.5| x 100} points apart at the extremes, so a strongly
 * quality-leaning (or speed-leaning) crew keeps its tier while a balanced crew
 * may use a warm neighbour tier.</p>
 *
 * <p>The triage phase has its own list ({@code KUBEMOOT_MODEL_CANDIDATES_TRIAGE}), and
 * each phase may carry a {@code preferFaster} weight
 * ({@code KUBEMOOT_MODEL_PREFER_FASTER_MULLING} and {@code _TRIAGE}) that favors the
 * candidate measured faster; see {@link ModelCandidates#rank}.</p>
 */
@ApplicationScoped
public class CandidatePolicy {

    /** Default tolerance in quality-score points. */
    public static final long DEFAULT_TOLERANCE = 10L;

    private final ModelCandidates mulling;
    private final ModelCandidates triage;
    private final long tolerance;
    private final long preferFasterMulling;
    private final long preferFasterTriage;

    @Inject
    public CandidatePolicy(ObjectMapper mapper,
                           @ConfigProperty(name = "kubemoot.model.candidates-mulling") Optional<String> mullingJson,
                           @ConfigProperty(name = "kubemoot.model.candidates-triage") Optional<String> triageJson,
                           @ConfigProperty(name = "kubemoot.model.candidate-tolerance",
                                   defaultValue = "" + DEFAULT_TOLERANCE) long tolerance,
                           @ConfigProperty(name = "kubemoot.model.prefer-faster-mulling",
                                   defaultValue = "0") long preferFasterMulling,
                           @ConfigProperty(name = "kubemoot.model.prefer-faster-triage",
                                   defaultValue = "0") long preferFasterTriage) {
        this.mulling = ModelCandidates.parse(mullingJson, mapper);
        this.triage = ModelCandidates.parse(triageJson, mapper);
        this.tolerance = tolerance;
        this.preferFasterMulling = preferFasterMulling;
        this.preferFasterTriage = preferFasterTriage;
    }

    /**
     * The models a mulling call may use: the one to place first and its alternatives,
     * with the rule's preferFaster applied against {@code speedOf}.
     */
    public ModelCandidates.Ranking mullingRanking(String bound, ToLongFunction<String> speedOf) {
        return mulling.rank(bound, tolerance, preferFasterMulling, speedOf);
    }

    /** The models a triage call may use, as {@link #mullingRanking} does for mulling. */
    public ModelCandidates.Ranking triageRanking(String bound, ToLongFunction<String> speedOf) {
        return triage.rank(bound, tolerance, preferFasterTriage, speedOf);
    }
}
