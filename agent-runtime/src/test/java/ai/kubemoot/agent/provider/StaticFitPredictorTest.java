package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link StaticFitPredictor}. In the weighted-cost scheduler
 * (see [[JIT Scheduler Locality Algorithm]]) {@code predict()} is the ONE hard
 * FEASIBILITY filter, not a ranking gate:
 * <ul>
 *   <li>warm (model resident or load-in-flight) -> always feasible; serving a
 *       call adds no VRAM. Contention/load/eviction are weights the
 *       {@link ProviderSelector} ranks on, never refusals here.</li>
 *   <li>cold -> feasible iff (a) the model alone fits the card's usable VRAM
 *       AND (b) the model fits alongside models already resident on the card.
 *       Condition (b) is the fix for [[Cold Fit Gate Ignores Resident Models]]:
 *       the old gate only checked (a), so it admitted a cold load onto a GPU
 *       already full of a different model, causing thrash or CPU spill.</li>
 * </ul>
 * Eviction-cost and locality ranking are covered in {@link ProviderSelectorTest}.
 */
class StaticFitPredictorTest {

    private final FitPredictor pred = new StaticFitPredictor();

    private static ProviderState state(String name, int maxParallel, boolean ready,
                                        long totalVramMiB, Map<String, Long> loaded) {
        return new ProviderState(name, "http://" + name, maxParallel, 0, 0,
                List.copyOf(loaded.keySet()), ready, "2026-05-28T00:00:00Z",
                totalVramMiB, loaded);
    }

    /** Cold-path FitInputs (model NOT resident); occupancy = the model's full footprint. */
    private static FitInputs in(ProviderState p, String model, int activeTickets,
                                long occupancyMiB, boolean circuitOpen) {
        return new FitInputs(p, model, activeTickets, 0L, occupancyMiB, 0L, 0.0, circuitOpen);
    }

    // ---- not feasible ----

    @Test
    void nullInputsRejected() {
        assertFalse(pred.predict(null).canServe(), "null FitInputs -> no");
    }

    @Test
    void circuitOpenRejected() {
        ProviderState p = state("rig0", 1, true, 32605, Map.of());
        assertFalse(pred.predict(in(p, "qwen3:8b", 0, 5979L, true)).canServe());
    }

    @Test
    void notReadyRejected() {
        ProviderState p = state("rig0", 1, false, 32605, Map.of());
        assertFalse(pred.predict(in(p, "qwen3:8b", 0, 5979L, false)).canServe());
    }

    @Test
    void unknownFootprintRejected() {
        ProviderState p = state("rig0", 1, true, 32605, Map.of());
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 0L, false));
        assertFalse(s.canServe());
        assertTrue(s.reasoning().contains("unknown"), s.reasoning());
    }

    @Test
    void unknownTotalVramRejected_forColdLoad() {
        // totalVram unset (operator pre-upgrade) and model not resident -> cannot
        // reason about cold fit.
        ProviderState p = state("rig0", 1, true, 0L, Map.of());
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 5979L, false));
        assertFalse(s.canServe());
        assertTrue(s.reasoning().contains("total VRAM"), s.reasoning());
    }

    // ---- warm = always feasible (no gate; weights handle the rest) ----

    @Test
    void warmIsFeasibleEvenWhenSlotBusy() {
        // Slot saturated, but the model is resident: still feasible. Queueing at
        // the warm home is a ranking cost, not a refusal.
        ProviderState p = state("rig0", 1, true, 32605, Map.of("qwen3:8b", 5979L));
        FitScore s = pred.predict(new FitInputs(p, "qwen3:8b", 8, 0L, 5979L, 0L, 0.0, false));
        assertTrue(s.canServe(), "warm + busy slot must stay feasible: " + s.reasoning());
        assertTrue(s.reasoning().contains("warm"), s.reasoning());
    }

    @Test
    void warmIsFeasibleEvenIfResidentSetIsLarge() {
        // A large warm model near the card's limit is STILL feasible to reuse -
        // reuse adds no VRAM, so size never disqualifies it. (A spilling warm
        // model is deprioritised by latency weight in ranking, not refused here.)
        ProviderState p = state("rig0", 1, true, 32605, Map.of("qwen3:32b", 31000L));
        FitScore s = pred.predict(new FitInputs(p, "qwen3:32b", 2, 0L, 31000L, 0L, 0.0, false));
        assertTrue(s.canServe(), "reusing a large warm model must be feasible: " + s.reasoning());
    }

    @Test
    void loadingInFlightIsFeasible() {
        // Model not resident yet, but an in-flight ticket is loading it here:
        // treat as warm-equivalent so a second caller converges instead of
        // triggering a redundant cold-load elsewhere.
        ProviderState p = state("rig0", 1, true, 32605, Map.of("qwen3:32b", 27252L));
        FitScore s = pred.predict(new FitInputs(
                p, "qwen3:8b", 1, 0L, 5979L, 0L, 0.0, false, java.util.Set.of("qwen3:8b")));
        assertTrue(s.canServe(), "load-in-flight should be feasible (convergence): " + s.reasoning());
        assertTrue(s.reasoning().contains("loading-in-flight"), s.reasoning());
    }

    // ---- cold = feasible iff occupancy fits usable VRAM ----

    @Test
    void coldFeasibleWhenFitsUsable() {
        // qwen3:8b (5979) cold on an empty 4090 (usable ~21616 of 24563): fits.
        ProviderState p = state("rig1", 1, true, 24563, Map.of());
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 5979L, false));
        assertTrue(s.canServe());
        assertTrue(s.reasoning().contains("cold-feasible"), s.reasoning());
    }

    @Test
    void coldRefusedWhenResidentLeavesNoRoom() {
        // The 2026-06-11 resident-blind gap, fixed. The 5090 has 32B resident at
        // 27252 MiB. An 8B cold-load would need 5979 MiB. Combined: 27252 + 5979
        // = 33231 MiB > usable 28693 MiB (= 32605 - 12% reserve). With the old
        // gate, which only checked 8B alone (5979 < 28693), this was ADMITTED.
        // With the new gate (cold-load + residents > usable -> refused), it is
        // correctly rejected - there is no room for a second model alongside the
        // resident 32B, even if Ollama were to do a full eviction. The invariant:
        // a cold load that would exceed usable VRAM even if nothing else were
        // resident is never feasible; and now: a cold load that would exceed
        // usable VRAM because of what IS resident is equally infeasible.
        ProviderState p = state("rig0", 1, true, 32605, Map.of("qwen3:32b", 27252L));
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 5979L, false));
        assertFalse(s.canServe(), "8B cold-load must be refused when 32B resident leaves no room: " + s.reasoning());
        assertTrue(s.reasoning().contains("no room alongside residents"), s.reasoning());
    }

    @Test
    void coldFeasibleWhenResidentLeavesEnoughRoom() {
        // Regression guard: when the resident model is small enough that the cold-
        // load candidate still fits, the gate must still ADMIT it. A small embedding
        // model (1000 MiB) on the 5090 leaves 27693 MiB for an 8B (5979 MiB).
        // 5979 + 1000 = 6979 < 28693. Admitted.
        ProviderState p = state("rig0", 1, true, 32605, Map.of("embed-nomic", 1000L));
        FitScore s = pred.predict(in(p, "qwen3:8b", 0, 5979L, false));
        assertTrue(s.canServe(), "8B must be admitted when resident leaves enough room: " + s.reasoning());
        assertTrue(s.reasoning().contains("cold-feasible"), s.reasoning());
    }

    @Test
    void coldFeasible_14b_on_4090_with_small_resident() {
        // Regression guard: a 14B (9000 MiB) cold-load on a 4090 (usable 21616)
        // with a small 1B embedding model resident (500 MiB). Combined: 500 + 9000
        // = 9500 < 21616. Gate 2 passes; gate 1 passes too (9000 < 21616). Admitted.
        ProviderState p = state("rig1", 1, true, 24563, Map.of("nomic-embed", 500L));
        FitScore s = pred.predict(in(p, "qwen3:14b", 0, 9000L, false));
        assertTrue(s.canServe(), "14B + small embed should fit 4090: " + s.reasoning());
    }

    @Test
    void coldRefused_14b_on_4090_when_8b_fills_it() {
        // Gate 2: 14B (9000) cold-load on a 4090 (usable 21616) already hosting
        // 8B at 14000 MiB. Combined: 14000 + 9000 = 23000 > 21616. Refused.
        // Gate 1 alone (9000 < 21616) would have admitted it - this is the gap.
        ProviderState p = state("rig1", 1, true, 24563, Map.of("qwen3:8b", 14000L));
        FitScore s = pred.predict(in(p, "qwen3:14b", 0, 9000L, false));
        assertFalse(s.canServe(), "14B must be refused when 8B fills the 4090: " + s.reasoning());
        assertTrue(s.reasoning().contains("no room alongside residents"), s.reasoning());
    }

    @Test
    void coldNotFeasibleWhenModelExceedsCard() {
        // qwen3:32b (occupancy 27252) cannot fit the 4090's usable VRAM (~21616)
        // even alone -> would spill to CPU -> never feasible on this GPU.
        ProviderState p = state("rig1", 1, true, 24563, Map.of());
        FitScore s = pred.predict(in(p, "qwen3:32b", 0, 27252L, false));
        assertFalse(s.canServe(), "32b must not be feasible on a 24 GiB card: " + s.reasoning());
        assertTrue(s.reasoning().contains("exceeds usable VRAM"), s.reasoning());
    }

    @Test
    void sameModelFeasibleOnLargerCard() {
        // The same 32b DOES fit the 5090's usable VRAM (~28693 of 32605).
        ProviderState p = state("rig0", 1, true, 32605, Map.of());
        FitScore s = pred.predict(in(p, "qwen3:32b", 0, 27252L, false));
        assertTrue(s.canServe(), "32b should fit the 32 GiB card: " + s.reasoning());
    }

    // ---- usableVramMiB reserve ----

    @Test
    void usableVramReservesSafetyMargin() {
        long usable4090 = StaticFitPredictor.usableVramMiB(24563L);
        assertTrue(usable4090 < 24563L, "usable < total");
        assertTrue(usable4090 >= 5979L, "but still admits an 8b: " + usable4090);
        long usable5090 = StaticFitPredictor.usableVramMiB(32605L);
        assertTrue(usable5090 > 27252L, "5090 usable must admit a 32b: " + usable5090);
        assertEquals(0L, StaticFitPredictor.usableVramMiB(0L), "unprobed -> 0");
    }

    // ---- coldFitsUsable (the shared cold-fit gate, exercised directly) ----

    @Test
    void coldFitsUsable_rejectsNonPositiveInputs() {
        assertFalse(StaticFitPredictor.coldFitsUsable(0L, 21616L, 0L), "zero footprint -> false");
        assertFalse(StaticFitPredictor.coldFitsUsable(-1L, 21616L, 0L), "negative footprint -> false");
        assertFalse(StaticFitPredictor.coldFitsUsable(9000L, 0L, 0L), "zero usable -> false");
        assertFalse(StaticFitPredictor.coldFitsUsable(9000L, -5L, 0L), "negative usable -> false");
    }

    @Test
    void coldFitsUsable_gate1_modelAlone() {
        assertTrue(StaticFitPredictor.coldFitsUsable(9000L, 21616L, 0L), "fits alone -> true");
        assertTrue(StaticFitPredictor.coldFitsUsable(21616L, 21616L, 0L), "exactly fits alone -> true");
        assertFalse(StaticFitPredictor.coldFitsUsable(21617L, 21616L, 0L), "exceeds card alone -> false");
    }

    @Test
    void coldFitsUsable_gate2_withResidents() {
        // residents present and the combined set still fits -> true
        assertTrue(StaticFitPredictor.coldFitsUsable(9000L, 21616L, 5000L), "fits alongside residents");
        // combined set exactly fills usable -> true (boundary, <=)
        assertTrue(StaticFitPredictor.coldFitsUsable(9000L, 21616L, 12616L), "exactly fills with residents");
        // combined set overflows usable -> false (Gate 2)
        assertFalse(StaticFitPredictor.coldFitsUsable(9000L, 21616L, 12617L), "overflows with residents");
        // residentOthers <= 0 means Gate 2 is skipped: Gate 1 alone decides
        assertTrue(StaticFitPredictor.coldFitsUsable(9000L, 21616L, 0L), "no residents -> Gate 1 only");
        assertTrue(StaticFitPredictor.coldFitsUsable(9000L, 21616L, -1L), "negative residents -> Gate 1 only");
    }

    @Test
    void recordOutcome_isNoop_inV1() {
        pred.recordOutcome("rig0", "qwen3:8b", 1500L, true);
        // no throw, no state change
        ProviderState p = state("rig0", 1, true, 32605, Map.of("qwen3:8b", 5979L));
        assertTrue(pred.predict(in(p, "qwen3:8b", 0, 5979L, false)).canServe());
    }
}
