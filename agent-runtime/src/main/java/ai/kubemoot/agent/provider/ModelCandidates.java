package ai.kubemoot.agent.provider;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Optional;

/**
 * The ranked candidate models the operator publishes for a phase
 * ({@code KUBEMOOT_MODEL_CANDIDATES_MULLING}, a JSON list of
 * {@code {"model": "...", "score": N}}, the bound model first). {@code score} is
 * the model's quality score under the crew's scheduling policy: the sum of the
 * matching {@code prefer} weights and the {@code qualityBias}/{@code latencyClass}
 * contribution.
 *
 * <p>The per-call pick uses {@link #warmAlternatives} to decide which other
 * models may replace the bound model when one of them is already warm with room:
 * only models whose score is within the quality tolerance of the bound model's
 * score qualify, so a crew that asks for quality does not drop to a much weaker
 * model to save a load.</p>
 */
public final class ModelCandidates {

    private static final Logger log = LoggerFactory.getLogger(ModelCandidates.class);

    /** One published candidate. */
    public record Candidate(String model, long score) {}

    private final List<Candidate> ranked;

    public ModelCandidates(List<Candidate> ranked) {
        this.ranked = List.copyOf(ranked);
    }

    /** No candidates: the bound model is the only choice. */
    public static ModelCandidates none() {
        return new ModelCandidates(List.of());
    }

    /**
     * Parses the operator's JSON list. Uses tree navigation (record
     * deserialization fails silently in GraalVM native). An absent, blank, or
     * malformed value yields {@link #none()}; entries without a model are skipped.
     */
    public static ModelCandidates parse(Optional<String> json, ObjectMapper mapper) {
        if (json.isEmpty() || json.get().isBlank()) {
            return none();
        }
        try {
            JsonNode root = mapper.readTree(json.get());
            List<Candidate> out = new ArrayList<>();
            if (root.isArray()) {
                root.forEach(n -> addCandidate(n, out));
            }
            return new ModelCandidates(out);
        } catch (Exception e) {
            log.warn("Ignoring unreadable model candidates: {}", e.getMessage());
            return none();
        }
    }

    private static void addCandidate(JsonNode n, List<Candidate> out) {
        String model = n.path("model").asText("");
        if (!model.isBlank()) {
            out.add(new Candidate(model, n.path("score").asLong(0L)));
        }
    }

    /** The published list, best first. */
    public List<Candidate> ranked() {
        return ranked;
    }

    /**
     * The other models worth using instead of {@code preferred} when one is warm
     * with room: every candidate except the preferred model whose score is at
     * least {@code preferredScore - tolerance}, highest score first. Empty when
     * the preferred model is not in the list.
     */
    public List<String> warmAlternatives(String preferred, long tolerance) {
        Optional<Candidate> pref = ranked.stream().filter(c -> c.model().equals(preferred)).findFirst();
        if (pref.isEmpty()) {
            return List.of();
        }
        long floor = pref.get().score() - Math.max(0L, tolerance);
        return ranked.stream()
                .filter(c -> !c.model().equals(preferred) && c.score() >= floor)
                .sorted(Comparator.comparingLong(Candidate::score).reversed())
                .map(Candidate::model)
                .distinct()
                .toList();
    }
}
