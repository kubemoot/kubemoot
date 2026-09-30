package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.TreeSet;

/**
 * Who a review wakes. A review always names its analysts; it never wakes every
 * analyst in the crew.
 *
 * <ul>
 *   <li>The analysts the coordinator selected for the question are the full
 *       review, and the best of them by resume match is the concurrence check.</li>
 *   <li>When none was selected, the best match by resume among all the crew's
 *       analysts is both.</li>
 * </ul>
 *
 * The resume match is the crew's resume search ranking for the question. Among
 * selected analysts that the ranking does not place, the first by name is taken
 * so the choice is deterministic; with no selected analyst and no ranking, there
 * is no one to wake.
 */
final class ReviewRoster {

    private ReviewRoster() {}

    /** The one analyst asked to concur, or an empty list when there is none. */
    static List<String> concurrer(Collection<String> selectedAnalysts, Collection<String> allAnalysts,
                                  List<String> ranking, Collection<String> exclude) {
        var selected = without(selectedAnalysts, exclude);
        if (!selected.isEmpty()) {
            return List.of(bestRanked(selected, ranking).orElse(selected.first()));
        }
        return bestRanked(without(allAnalysts, exclude), ranking).map(List::of).orElse(List.of());
    }

    /** The analysts of a full review: the selected ones, or else the best match. */
    static List<String> full(Collection<String> selectedAnalysts, Collection<String> allAnalysts,
                             List<String> ranking, Collection<String> exclude) {
        var selected = without(selectedAnalysts, exclude);
        if (!selected.isEmpty()) {
            return new ArrayList<>(selected);
        }
        return concurrer(List.of(), allAnalysts, ranking, exclude);
    }

    /** The first name in {@code ranking} that is among {@code candidates}. */
    static Optional<String> bestRanked(Set<String> candidates, List<String> ranking) {
        if (ranking == null) {
            return Optional.empty();
        }
        return ranking.stream().filter(candidates::contains).findFirst();
    }

    private static TreeSet<String> without(Collection<String> names, Collection<String> exclude) {
        var result = new TreeSet<String>();
        if (names != null) {
            result.addAll(names);
        }
        if (exclude != null) {
            result.removeAll(exclude);
        }
        return result;
    }
}
