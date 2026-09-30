package ai.kubemoot.agent.provider;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

/**
 * Chooses which idle resident models to unload on one provider so a cold load
 * fits. Pure, for testing.
 *
 * <ul>
 *   <li>Never a model with in-flight work (or one another call already plans to unload).</li>
 *   <li>Models with waiting agents are kept unless no choice without them makes room.</li>
 *   <li>The fewest victims that make room; among equally few, the least needed:
 *       fewer intents, then lower recent use plus prediction, then least recently used
 *       ({@link ModelDemand#LEAST_NEEDED_FIRST}).</li>
 * </ul>
 */
public final class EvictionPlanner {

    private EvictionPlanner() {}

    /** Idle resident models considered per provider; more than this is not searched. */
    static final int MAX_CANDIDATES = 10;

    /**
     * Victims that make room for {@code needMiB} on a provider with {@code freeMiB}
     * free, or empty when no choice of idle residents does. An empty list inside the
     * optional means the model already fits.
     */
    public static Optional<List<PlacementCostModel.Victim>> chooseVictims(
            Map<String, Long> residents, Set<String> unavailable, Map<String, ModelDemand> demand,
            long freeMiB, long needMiB) {
        long shortfall = needMiB - freeMiB;
        if (shortfall <= 0) {
            return Optional.of(List.of());
        }
        List<PlacementCostModel.Victim> idle = idleResidents(residents, unavailable, demand);
        List<PlacementCostModel.Victim> withoutWaiters = idle.stream().filter(v -> v.demand().waiters() == 0).toList();
        Optional<List<PlacementCostModel.Victim>> pick = fewestThatFit(withoutWaiters, shortfall);
        return pick.isPresent() ? pick : fewestThatFit(idle, shortfall);
    }

    private static List<PlacementCostModel.Victim> idleResidents(
            Map<String, Long> residents, Set<String> unavailable, Map<String, ModelDemand> demand) {
        List<PlacementCostModel.Victim> idle = new ArrayList<>();
        residents.forEach((model, fp) -> {
            if (!unavailable.contains(model) && fp != null && fp > 0) {
                idle.add(new PlacementCostModel.Victim(model, fp, demand.getOrDefault(model, ModelDemand.NONE)));
            }
        });
        idle.sort(Comparator.comparing(PlacementCostModel.Victim::demand, ModelDemand.LEAST_NEEDED_FIRST)
                .thenComparing(PlacementCostModel.Victim::model));
        return idle.size() > MAX_CANDIDATES ? idle.subList(0, MAX_CANDIDATES) : idle;
    }

    /** The first (least-needed-first) combination of the smallest size whose footprints cover {@code shortfall}. */
    private static Optional<List<PlacementCostModel.Victim>> fewestThatFit(
            List<PlacementCostModel.Victim> ordered, long shortfall) {
        for (int k = 1; k <= ordered.size(); k++) {
            Optional<List<PlacementCostModel.Victim>> found =
                    firstCombination(ordered, k, 0, new ArrayList<>(), shortfall);
            if (found.isPresent()) {
                return found;
            }
        }
        return Optional.empty();
    }

    /** The first combination of {@code k} victims, extending {@code chosen}, that covers {@code shortfall}. */
    private static Optional<List<PlacementCostModel.Victim>> firstCombination(
            List<PlacementCostModel.Victim> ordered, int k, int from,
            List<PlacementCostModel.Victim> chosen, long shortfall) {
        if (chosen.size() == k) {
            long freed = chosen.stream().mapToLong(PlacementCostModel.Victim::footprintMiB).sum();
            return freed >= shortfall ? Optional.of(List.copyOf(chosen)) : Optional.empty();
        }
        for (int i = from; i < ordered.size(); i++) {
            chosen.add(ordered.get(i));
            Optional<List<PlacementCostModel.Victim>> found = firstCombination(ordered, k, i + 1, chosen, shortfall);
            chosen.remove(chosen.size() - 1);
            if (found.isPresent()) {
                return found;
            }
        }
        return Optional.empty();
    }
}
