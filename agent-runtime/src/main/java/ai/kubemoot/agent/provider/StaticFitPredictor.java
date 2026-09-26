package ai.kubemoot.agent.provider;

/**
 * Baseline {@link FitPredictor}: the ONE hard FEASIBILITY filter of the
 * weighted-cost scheduler (see {@code docs/architecture/scheduler.md} and
 * [[JIT Scheduler Locality Algorithm]]). Composed by {@link LearningFitPredictor}
 * (the CDI default) for the canServe decision; can also stand alone in tests.
 * Not a CDI bean - LearningFitPredictor is the auto-discovered default and this
 * class is instantiated only by explicit composition or by tests.
 *
 * <p>{@code predict()} answers only "can this provider serve model M at all?"
 * It is NOT a ranking gate; load, contention, spill, and eviction are weights the
 * {@link ProviderSelector} sums to choose among feasible providers.
 *
 * <h3>Feasibility</h3>
 * <ol>
 *   <li>circuit open / provider not ready -> {@code canServe = false}</li>
 *   <li>unknown model footprint or unknown total VRAM -> {@code false} (cannot reason)</li>
 *   <li>WARM (model resident or load-in-flight) -> {@code true} always. Serving a
 *       call adds no VRAM (Ollama reuses the model's slots within its existing KV
 *       slab). A busy or spilling warm provider is deprioritised by the selector's
 *       weights, never refused here.</li>
 *   <li>COLD (model not resident) -> {@code true} iff BOTH:
 *       (a) {@code occupancy(M) <= usableVram(P)}: M physically fits the card alone,
 *       (b) {@code occupancy(M) + residentOthers(P) <= usableVram(P)}: M fits alongside
 *       the models already loaded on the card. If (b) fails, loading M forces Ollama
 *       to evict a resident and the combined set still cannot fit - refuse rather than
 *       let Ollama thrash or spill. See [[Cold Fit Gate Ignores Resident Models]].
 *       {@code occupancy} is the model's full KV-INCLUSIVE footprint; Ollama reserves
 *       the whole context slab at load, so there is no separate per-call KV term.
 *       Whether a load would EVICT a resident model (when there IS room after eviction)
 *       is an eviction COST in the selector, not a feasibility question here.</li>
 * </ol>
 *
 * <p>{@code thisCallKvCacheEstimateMiB} and {@code activeKvCacheReservedMiB} on
 * {@link FitInputs} are vestigial under this model (KV lives inside occupancy) and
 * are ignored. {@code successProbability} / observed latency are echoed for the
 * selector's reliability and health weights.
 */
public class StaticFitPredictor implements FitPredictor {

    /**
     * Fraction of a provider's total VRAM held in reserve for GPU/runtime
     * overhead (CUDA context), KV-cache growth beyond the per-call estimate,
     * and fragmentation. A cold-load is refused when weights + KV would consume
     * more than {@code total - reserve}. Without this, a 32B model (~22 GiB in
     * VRAM) numerically "fits" a 24 GiB 4090 and the gate admits it, but Ollama
     * then spills ~20% to CPU (glacial discussions). Reserving ~12% makes the
     * gate match reality: 32B refused on the 24 GiB 4090, allowed on the 32 GiB
     * 5090; 8B/14B still fit both. Reproduced live 2026-06-11. Model-agnostic and
     * rig-agnostic (a fraction of each provider's own total). See
     * [[JIT Fit-Gate Degraded Mode Can Spill]].
     */
    static final double VRAM_SAFETY_FRACTION = 0.12;
    static final long VRAM_SAFETY_FLOOR_MIB = 2048;

    /** Usable VRAM for a cold-load fit decision: total minus the safety reserve. */
    static long usableVramMiB(long totalVramMiB) {
        if (totalVramMiB <= 0) return 0L;
        long reserve = Math.max(VRAM_SAFETY_FLOOR_MIB, (long) (totalVramMiB * VRAM_SAFETY_FRACTION));
        return Math.max(0L, totalVramMiB - reserve);
    }

    /**
     * Single source of truth for the cold-load fit gate, shared by {@link #predict}
     * (the JIT selection path) and OllamaDirectProber.pickFittingEndpoint (the
     * NATS-degraded fallback path) so the two cannot diverge. Two gates:
     *   Gate 1: the model fits the card alone (footprint <= usable).
     *   Gate 2: it fits alongside the OTHER models already resident
     *           (footprint + residentOthers <= usable).
     * footprintMiB must be the inflated/real-VRAM estimate; residentOthersMiB is the
     * summed footprint of models already resident here (0 = empty card / unknown).
     */
    static boolean coldFitsUsable(long footprintMiB, long usableVramMiB, long residentOthersMiB) {
        if (footprintMiB <= 0 || usableVramMiB <= 0) return false;
        if (footprintMiB > usableVramMiB) return false; // Gate 1
        // Gate 2: with residents present, M plus residents must still fit usable.
        return residentOthersMiB <= 0 || footprintMiB + residentOthersMiB <= usableVramMiB;
    }

    @Override
    public FitScore predict(FitInputs in) {
        if (in == null || in.provider() == null) {
            return FitScore.no("invalid inputs");
        }
        if (in.circuitOpen()) {
            return FitScore.no("circuit open");
        }
        if (!in.provider().ready()) {
            return FitScore.no("provider not ready");
        }
        if (in.coldLoadFootprintMiB() <= 0) {
            // No model-size estimate available (no provider has loaded
            // it yet AND no CR hint). Selector falls back to static
            // endpoint via caller; predictor reports honestly.
            return FitScore.no("unknown cold-load footprint");
        }

        boolean trulyWarm = in.provider().hasModelLoaded(in.modelName());
        // 2026-05-31: also recognise "load-in-flight" via an active ticket
        // for the same model. Treating this as warm-equivalent lets a
        // second agent arriving during the cold-load window converge on
        // the in-flight provider (Ollama internally queues the call) instead
        // of triggering a redundant cold-load on a different provider. See
        // [[Cold-Start Model-Load Wedge]] for the design and trade-offs.
        boolean loadingInFlight = !trulyWarm
                && in.modelName() != null
                && in.activeModelsOnProvider() != null
                && in.activeModelsOnProvider().contains(in.modelName());
        boolean warmOrLoading = trulyWarm || loadingInFlight;

        if (warmOrLoading) {
            // Warm (resident) or load-in-flight: M is already on this GPU, or
            // arriving, so serving this call adds NO VRAM - Ollama reuses the
            // model's slots within its existing KV slab. Always FEASIBLE here.
            // Contention and load are weights the selector ranks on, not gates:
            // a busy warm card is deprioritised, never excluded (queue at home
            // beats migrate-and-evict). A warm model that is spilling surfaces as
            // high observed latency and is ranked down by the health weight, not
            // refused here. See [[JIT Scheduler Locality Algorithm]].
            return FitScore.yes((long) in.observedLatencyMs(),
                    trulyWarm ? "warm (resident, reuse)" : "loading-in-flight (converging)");
        }

        // Cold: M must be loaded here. Two hard filters:
        // 1. M physically fits the card alone: usableVram >= coldLoadFootprintMiB.
        //    If not, M spills to CPU even when alone - never feasible here.
        // 2. M fits alongside models already resident on the card:
        //    residentOthers + coldLoadFootprintMiB <= usable.
        //    If not, loading M forces Ollama to evict a resident model AND the
        //    combined set still cannot fit. This closes the "cold-gate ignores
        //    resident models" gap: the old gate only checked M alone, so it
        //    admitted a cold load onto a GPU already full of a different model
        //    (e.g. 32B warm on 5090 at 27252 MiB + 8B cold at 5979 MiB = 33231
        //    MiB > 32605 total). See [[Cold Fit Gate Ignores Resident Models]].
        //
        //    NOTE: M is cold here (not in loadedModelFootprintsMiB), so
        //    loadedFootprintSumMiB() covers only the OTHER resident models -
        //    exactly the pressure we need to account for.
        //
        //    Eviction COST (selector weighs it when there IS room after eviction)
        //    is separate; this gate blocks ONLY the physically-impossible case
        //    where no eviction strategy produces enough room for M.
        long totalVram = in.provider().totalVramMiB();
        if (totalVram <= 0) {
            return FitScore.no("unknown total VRAM");
        }
        long usable = usableVramMiB(totalVram);
        long occupancy = in.coldLoadFootprintMiB();
        // M is cold (absent from loadedModelFootprintsMiB), so loadedFootprintSumMiB
        // covers only the OTHER resident models - exactly the pressure to account for.
        long residentOthers = in.provider().loadedFootprintSumMiB();
        // Decision single-sourced in coldFitsUsable (shared with the NATS-degraded
        // path); derive the specific refusal message from which gate failed.
        if (!coldFitsUsable(occupancy, usable, residentOthers)) {
            if (occupancy > usable) {
                return FitScore.no(String.format(
                        "model footprint %d MiB exceeds usable VRAM (%d of %d total) - cannot fit this GPU",
                        occupancy, usable, totalVram));
            }
            return FitScore.no(String.format(
                    "cold-load %d MiB + resident %d MiB = %d MiB exceeds usable VRAM %d MiB - no room alongside residents",
                    occupancy, residentOthers, occupancy + residentOthers, usable));
        }
        return FitScore.yes((long) in.observedLatencyMs(), String.format(
                "cold-feasible (footprint %d MiB + resident %d MiB <= usable %d of %d total)",
                occupancy, residentOthers, usable, totalVram));
    }

    @Override
    public void recordOutcome(String provider, String modelName, long latencyMs, boolean success) {
        // v1: no-op. Learning loop ships in v2.
    }
}
