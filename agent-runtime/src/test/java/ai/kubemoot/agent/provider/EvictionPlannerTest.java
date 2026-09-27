package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;

/** Victim choice: never in-flight, waiters last, then intents, rate, and least recently used; fewest victims. */
class EvictionPlannerTest {

    private static List<String> victims(Map<String, Long> residents, Set<String> busy,
                                        Map<String, ModelDemand> demand, long free, long need) {
        return EvictionPlanner.chooseVictims(residents, busy, demand, free, need)
                .map(v -> v.stream().map(PlacementCostModel.Victim::model).toList())
                .orElse(null);
    }

    private static ModelDemand used(double rate, long lastUsedMs) {
        return new ModelDemand(0, 0, rate, 0.0, lastUsedMs);
    }

    @Test
    void fitsWithoutEviction_needsNoVictims() {
        assertEquals(List.of(), victims(Map.of("a", 10_000L), Set.of(), Map.of(), 12_000, 12_000));
    }

    @Test
    void modelWithInFlightWork_isNeverUnloaded() {
        assertNull(victims(Map.of("busy", 20_000L), Set.of("busy"), Map.of(), 2_000, 12_000),
                "the only model that would make room has in-flight work");
        assertEquals(List.of("idle"), victims(Map.of("busy", 20_000L, "idle", 12_000L), Set.of("busy"),
                Map.of(), 2_000, 12_000));
    }

    @Test
    void modelAgentsWaitFor_isKeptWhenAnotherChoiceExists() {
        var demand = Map.of("wanted", new ModelDemand(1, 0, 0, 0, 0), "spare", ModelDemand.NONE);
        assertEquals(List.of("spare"), victims(Map.of("wanted", 12_000L, "spare", 12_000L), Set.of(),
                demand, 0, 10_000));
    }

    @Test
    void modelAgentsWaitFor_isUnloadedOnlyWhenEveryChoiceHasWaiters() {
        var demand = Map.of("a", new ModelDemand(2, 0, 0, 0, 0), "b", new ModelDemand(1, 0, 0, 0, 0));
        assertEquals(List.of("b"), victims(Map.of("a", 12_000L, "b", 12_000L), Set.of(), demand, 0, 10_000),
                "fewer waiters goes first");
    }

    @Test
    void intentsOutrankRecentUse() {
        var demand = Map.of("intended", new ModelDemand(0, 1, 0, 0, 0), "busyLately", used(5.0, 1_000));
        assertEquals(List.of("busyLately"), victims(Map.of("intended", 12_000L, "busyLately", 12_000L),
                Set.of(), demand, 0, 10_000));
    }

    @Test
    void lowerRecentUseGoesFirst_thenLeastRecentlyUsed() {
        var byRate = Map.of("hot", used(3.0, 1_000), "cool", used(0.5, 9_000));
        assertEquals(List.of("cool"), victims(Map.of("hot", 12_000L, "cool", 12_000L), Set.of(), byRate, 0, 10_000));
        var byAge = Map.of("recent", used(0.0, 9_000), "old", used(0.0, 1_000));
        assertEquals(List.of("old"), victims(Map.of("recent", 12_000L, "old", 12_000L), Set.of(), byAge, 0, 10_000));
    }

    @Test
    void predictionCountsLikeRecentUse() {
        var demand = Map.of("forecast", new ModelDemand(0, 0, 0, 2.0, 0), "quiet", ModelDemand.NONE);
        assertEquals(List.of("quiet"), victims(Map.of("forecast", 12_000L, "quiet", 12_000L), Set.of(), demand, 0, 10_000));
    }

    @Test
    void fewestVictims_beatsTwoLessNeededOnes() {
        // Two small unwanted models could make room together, but one larger model alone is enough.
        var demand = Map.of("big", used(0.2, 5_000), "s1", ModelDemand.NONE, "s2", ModelDemand.NONE);
        assertEquals(List.of("big"), victims(Map.of("big", 14_000L, "s1", 6_000L, "s2", 6_000L), Set.of(),
                demand, 0, 12_000));
    }

    @Test
    void usesSeveralVictimsWhenNoSingleOneIsEnough() {
        assertEquals(List.of("a", "b"), victims(Map.of("a", 6_000L, "b", 6_000L, "c", 6_000L), Set.of(),
                Map.of("c", used(1.0, 1_000)), 0, 12_000));
    }

    @Test
    void noChoiceMakesRoom_isEmpty() {
        assertNull(victims(Map.of("a", 2_000L), Set.of(), Map.of(), 0, 12_000));
    }
}
