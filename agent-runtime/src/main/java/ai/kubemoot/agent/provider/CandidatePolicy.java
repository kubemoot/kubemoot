package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.eclipse.microprofile.config.inject.ConfigProperty;

import java.util.List;
import java.util.Optional;

/**
 * Which other models a mulling call may use instead of the agent's bound model.
 *
 * <p>Reads the operator-published candidate list
 * ({@code KUBEMOOT_MODEL_CANDIDATES_MULLING}) and the quality tolerance
 * ({@code KUBEMOOT_MODEL_CANDIDATE_TOLERANCE}, default
 * {@value #DEFAULT_TOLERANCE} score points). A candidate qualifies when its
 * quality score is at most the tolerance below the bound model's score. With the
 * {@code qualityBias}/{@code latencyClass} scoring, one latencyClass tier is
 * {@code |bias - 0.5| x 100} points apart at the extremes, so a strongly
 * quality-leaning (or speed-leaning) crew keeps its tier while a balanced crew
 * may use a warm neighbour tier.</p>
 */
@ApplicationScoped
public class CandidatePolicy {

    /** Default tolerance in quality-score points. */
    public static final long DEFAULT_TOLERANCE = 10L;

    private final ModelCandidates mulling;
    private final long tolerance;

    @Inject
    public CandidatePolicy(ObjectMapper mapper,
                           @ConfigProperty(name = "kubemoot.model.candidates-mulling") Optional<String> mullingJson,
                           @ConfigProperty(name = "kubemoot.model.candidate-tolerance",
                                   defaultValue = "" + DEFAULT_TOLERANCE) long tolerance) {
        this.mulling = ModelCandidates.parse(mullingJson, mapper);
        this.tolerance = tolerance;
    }

    /** Warm-with-room alternatives to {@code preferred} for a mulling call, best first. */
    public List<String> mullingAlternatives(String preferred) {
        return mulling.warmAlternatives(preferred, tolerance);
    }
}
