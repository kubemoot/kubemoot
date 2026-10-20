package ai.kubemoot.agent.modelserver;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Optional;
import java.util.function.ToLongFunction;

/**
 * The ranked candidate models the operator publishes for a phase
 * ({@code KUBEMOOT_MODEL_CANDIDATES_MULLING}, a JSON list of
 * {@code {"model": "...", "score": N}}, the bound model first). {@code score} is
 * the model's quality score under the crew's scheduling policy: the sum of the
 * matching {@code prefer} weights and the {@code qualityBias} (model size) and
 * ability contribution.
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

    /**
     * The models one call may use: the one to place first and the others that may
     * stand in for it, best first.
     */
    public record Ranking(String preferred, List<String> alternatives) {
        /** The preferred model, then the alternatives. */
        public List<String> acceptable() {
            List<String> all = new ArrayList<>();
            all.add(preferred);
            all.addAll(alternatives);
            return all;
        }
    }

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

    /**
     * Ranks the candidates for one call. With {@code preferFaster} points and at least
     * one measured speed, each candidate's score is raised by {@code preferFaster}
     * times its measured speed over the fastest candidate's, and the call prefers the
     * best of the raised scores (the bound model on a tie); the tolerance applies to
     * the raised scores. A candidate with no measured speed gets no points: it is never
     * guessed fast or slow. With {@code preferFaster} 0, no measured speed, or a bound
     * model that is not listed, the bound model stays preferred and the alternatives are
     * {@link #warmAlternatives}, as before.
     *
     * @param speedOf measured generation speed of a model, tokens per second; 0 when
     *                not measured
     */
    public Ranking rank(String bound, long tolerance, long preferFaster, ToLongFunction<String> speedOf) {
        Optional<Candidate> boundEntry = ranked.stream().filter(c -> c.model().equals(bound)).findFirst();
        long fastest = ranked.stream().mapToLong(c -> speedOf.applyAsLong(c.model())).max().orElse(0L);
        if (boundEntry.isEmpty() || preferFaster <= 0 || fastest <= 0) {
            return new Ranking(bound, warmAlternatives(bound, tolerance));
        }
        List<Candidate> raised = ranked.stream()
                .map(c -> new Candidate(c.model(), c.score() + speedPoints(speedOf.applyAsLong(c.model()), fastest, preferFaster)))
                .toList();
        Candidate best = bestOf(raised, bound);
        long floor = best.score() - Math.max(0L, tolerance);
        List<String> alternatives = raised.stream()
                .filter(c -> !c.model().equals(best.model()) && c.score() >= floor)
                .sorted(Comparator.comparingLong(Candidate::score).reversed())
                .map(Candidate::model)
                .distinct()
                .toList();
        return new Ranking(best.model(), alternatives);
    }

    /** The points a speed earns: {@code preferFaster} in proportion to {@code speed} over {@code fastest}. */
    static long speedPoints(long speed, long fastest, long preferFaster) {
        if (speed <= 0 || fastest <= 0) {
            return 0L;
        }
        return Math.round((double) preferFaster * speed / fastest);
    }

    /** The highest-scoring candidate; the bound model keeps a tie, then list order decides. */
    private static Candidate bestOf(List<Candidate> candidates, String bound) {
        Candidate best = candidates.stream().filter(c -> c.model().equals(bound)).findFirst().orElse(candidates.get(0));
        for (Candidate c : candidates) {
            if (c.score() > best.score()) {
                best = c;
            }
        }
        return best;
    }
}
