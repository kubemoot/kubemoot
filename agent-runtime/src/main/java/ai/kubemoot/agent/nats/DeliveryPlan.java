package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * What the coordinator does before the synthesizer reads, decided from state: the stored results no
 * analyst read ({@link EvidenceCoverage#uncovered}), the room the synthesizer has for them, and the
 * analysts that can reduce evidence with a tool. Nothing here depends on a timer or a keyword.
 *
 * <ul>
 *   <li>{@link Action#SYNTHESIZE}: everything is covered, or what is left fits the room, or no analyst
 *       can take it (the crew has none with a query or code tool, or the rounds of convening are used):
 *       the synthesizer is given the uncovered results as far as they fit, with refinement as the
 *       fallback for the rest.</li>
 *   <li>{@link Action#CONVENE}: what is left does not fit and a query-capable analyst can take it: the
 *       analysts in the assignments read the groups named for them, in parallel.</li>
 * </ul>
 */
record DeliveryPlan(Action action, List<Assignment> assignments) {

    enum Action { SYNTHESIZE, CONVENE }

    /** The groups of uncovered calls one analyst is asked to read: each as its key pattern and uncovered call count. */
    record Assignment(String analyst, Map<String, Integer> patterns) {}

    /** Most times the coordinator convenes analysts for one thread. */
    static final int MAX_CONVENINGS = 1;

    static DeliveryPlan synthesize() {
        return new DeliveryPlan(Action.SYNTHESIZE, List.of());
    }

    /**
     * Decides from the uncovered calls, whether the synthesis holds them whole in one round
     * ({@link StoredDelivery#fitsOneRound}, the test the fill itself uses), the analysts able to run a
     * query or code and the number of times analysts were already convened.
     */
    static DeliveryPlan decide(List<ThreadCatalog.Entry> uncovered, boolean fitsOneRound,
                               List<String> queryAnalysts, int convenings) {
        if (uncovered.isEmpty() || fitsOneRound || queryAnalysts.isEmpty() || convenings >= MAX_CONVENINGS) {
            return synthesize();
        }
        List<Assignment> assignments = assign(uncovered, queryAnalysts);
        return assignments.isEmpty() ? synthesize() : new DeliveryPlan(Action.CONVENE, assignments);
    }

    /** The groups of uncovered calls shared out one by one, largest first, so the readers finish together. */
    static List<Assignment> assign(List<ThreadCatalog.Entry> uncovered, List<String> analysts) {
        var groups = new LinkedHashMap<String, Integer>();
        var bytes = new LinkedHashMap<String, Long>();
        for (ThreadCatalog.Entry e : uncovered) {
            if (e.error()) {
                continue;
            }
            groups.merge(e.pattern(), 1, Integer::sum);
            bytes.merge(e.pattern(), e.bytes(), Long::sum);
        }
        var order = new ArrayList<>(groups.keySet());
        order.sort((a, b) -> Long.compare(bytes.get(b), bytes.get(a)));
        var loads = new LinkedHashMap<String, Map<String, Integer>>();
        var sizes = new LinkedHashMap<String, Long>();
        analysts.forEach(a -> {
            loads.put(a, new LinkedHashMap<>());
            sizes.put(a, 0L);
        });
        for (String pattern : order) {
            String lightest = analysts.stream().min((a, b) -> Long.compare(sizes.get(a), sizes.get(b))).orElseThrow();
            loads.get(lightest).put(pattern, groups.get(pattern));
            sizes.merge(lightest, bytes.get(pattern), Long::sum);
        }
        return loads.entrySet().stream().filter(e -> !e.getValue().isEmpty())
                .map(e -> new Assignment(e.getKey(), e.getValue())).toList();
    }

    boolean convenes() {
        return action == Action.CONVENE;
    }
}
