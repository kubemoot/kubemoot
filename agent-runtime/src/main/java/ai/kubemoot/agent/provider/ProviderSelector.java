package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.nats.NatsConnectionProvider;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import io.nats.client.KeyValue;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Optional;
import java.util.concurrent.atomic.AtomicReference;

/**
 * Just-in-time provider selection for inference calls.
 *
 * Reads the {@code kubemoot_provider_state} NATS KV bucket on each call,
 * filters to ready providers that have the desired model loaded (warm),
 * and picks the one with the lowest saturation ratio (activeCount /
 * maxParallel). Cold-model providers are a fallback when no warm
 * provider is available.
 *
 * Design context: this is the JIT-scheduling primitive replacing the
 * operator's static per-Agent provider binding. See
 * tasks/notes/Epic - JIT GPU Scheduling.md and the
 * [[feedback_jit_gpu_scheduling]] memory for the architectural intent.
 * Bin-packing across providers is now emergent — each call picks the
 * freest provider given current shared state, just like kube-scheduler
 * picks the best Node when a Pod becomes Pending.
 *
 * <h3>Atomic claim/release (not yet implemented)</h3>
 * This v1 reads the operator-published {@code activeCount} (probe-time,
 * up to 30s stale). Under heavy concurrent load, two agents can read
 * stale state and both pick the same provider before its activeCount
 * updates — they'll both run; ollama queues; not broken but not
 * optimally distributed. A follow-up will add atomic NATS KV CAS
 * increment/decrement around each call for per-call accuracy.
 *
 * <h3>Caching</h3>
 * The KV reads are sub-millisecond on a warm NATS connection, but during
 * a burst of concurrent inference calls (the tooler fan-out phase
 * fires N agents in parallel), they'd hammer NATS. A short in-memory
 * cache (200ms TTL) absorbs that without making the state meaningfully
 * stale — provider load doesn't change faster than one inference per
 * 200ms anyway.
 *
 * <h3>Fallback</h3>
 * NATS unreachable or bucket empty → returns Optional.empty(); callers
 * fall back to their current static endpoint. Never blocks an inference
 * call on NATS health.
 */
@ApplicationScoped
public class ProviderSelector {

    private static final Logger log = LoggerFactory.getLogger(ProviderSelector.class);

    /** NATS KV bucket name — must match operator's ProviderStateBucket constant. */
    static final String STATE_BUCKET = "kubemoot_provider_state";

    /** In-memory cache TTL for KV reads to absorb burst load. */
    private static final Duration CACHE_TTL = Duration.ofMillis(200);

    /**
     * Ignore provider-state entries whose {@code lastProbedAt} is older than this.
     * The operator probes every ~30s; an entry minutes stale is an orphan (deleted
     * ModelProvider CR whose KV state was never reaped, or a leftover test fixture)
     * and must not be ranked - it pollutes placement with a phantom GPU. Generous
     * (10x the probe cycle) so a momentary operator hiccup never drops a real one.
     */
    static final Duration STATE_STALE_AFTER = Duration.ofMinutes(5);

    /** True when a provider-state lastProbedAt timestamp is older than {@link #STATE_STALE_AFTER}. */
    static boolean isStaleState(String lastProbedAt) {
        if (lastProbedAt == null || lastProbedAt.isEmpty()) return false; // unknown - tolerant, keep
        try {
            return Instant.parse(lastProbedAt).isBefore(Instant.now().minus(STATE_STALE_AFTER));
        } catch (Exception e) {
            return false; // unparseable - keep rather than over-filter
        }
    }

    private final NatsConnectionProvider natsProvider;
    private final ObjectMapper objectMapper;
    private final TicketManager ticketManager;
    private final FitPredictor fitPredictor;
    /** The model server's lifecycle driver: concurrency and model-switch costs. */
    private final EngineDriver driver;
    /**
     * Default predictor for the static rankCandidates path when no predictor
     * is supplied. v2 uses the learning impl; v1 baseline is reachable by
     * tests via {@link StaticFitPredictor} directly.
     */
    static final FitPredictor DEFAULT_PREDICTOR = new LearningFitPredictor();

    /** The error from the last failed provider-state read; null after a successful read. */
    private volatile String lastReadError;

    /**
     * Why the last {@link #readState} returned nothing, when it was a read error
     * rather than an empty bucket; empty when the last read succeeded.
     */
    public Optional<String> lastReadError() {
        return Optional.ofNullable(lastReadError);
    }

    /** Cached snapshot of all provider states; refreshed on TTL expiry. */
    private final AtomicReference<CachedSnapshot> cache = new AtomicReference<>();

    /**
     * Self-observed recent latency per provider endpoint, in milliseconds.
     * Captures the agent's actual experience calling each provider —
     * which catches false-warm situations (state says model loaded but
     * ollama actually evicted and cold-loads on the next call). Updated
     * via {@link #recordObservedLatency} after each call; consulted in
     * {@link #pickFor} to penalise providers that recently took long.
     *
     * Self-correcting: a single bad call records the slowness, the next
     * pick avoids that provider, the bad provider gets a chance to
     * recover (its entry decays over time), the loop balances.
     *
     * EMA-smoothed via {@link #LATENCY_EMA_ALPHA} so a single fast/slow
     * call doesn't whipsaw the scoring. Initial observations carry
     * weight 1.0 (first sample is the EMA seed).
     */
    private final java.util.concurrent.ConcurrentHashMap<String, Double> recentLatency =
            new java.util.concurrent.ConcurrentHashMap<>();

    /** EMA smoothing factor — higher = more weight on recent observations. */
    private static final double LATENCY_EMA_ALPHA = 0.4;

    /**
     * Latency penalty scale, in saturation-ratio units per 1000ms. Tuned
     * so that a 30s observed latency adds ~3.0 saturation-equivalent
     * penalty, comparable to a provider with 3 agents queued ahead. Picks
     * still prefer fresh-fast over saturated-fast, but a provider that
     * just took 30s loses to a free alternative even if state says both
     * are warm.
     */
    private static final double LATENCY_PENALTY_PER_SECOND = 0.1;

    // ============================================================================
    // AI Circuit Breaker — Phase A of the post-d61647e9 reliability work.
    // ============================================================================
    //
    // When an Ollama instance returns 5xx or times out repeatedly (the wedge
    // case observed 2026-05-28 on rig1: HTTP 500 after 2 min on /api/chat with
    // GPU idle), the agent's static "filter by ready" doesn't help — the
    // operator's probe-time Ready field is updated on a 30s cycle, much
    // slower than the failure cadence. v2.1's recordObservedLatency
    // penalises the bad provider on the score, but it still gets picked.
    //
    // The circuit breaker is a hard exclusion: after N consecutive failures
    // on a provider within a sliding window, treat it as open (unhealthy)
    // for COOLDOWN_SECONDS and exclude it from rankCandidates. After the
    // cooldown, half-open — try one call; success closes the circuit,
    // failure re-opens for another cooldown window.
    //
    // Per-agent local. Faster reaction than operator-side aggregation, no
    // NATS coordination needed. Trade-off: each agent learns independently,
    // so a fleet-wide outage takes N failures × M agents to fully detect.
    // Per [[AI Circuit Breaker]] backlog card; this is the v1 inline cut.
    /** Failures within {@link #CIRCUIT_FAILURE_WINDOW} before the circuit opens. */
    static final int CIRCUIT_FAILURE_THRESHOLD = 3;
    /** Sliding window over which {@link #CIRCUIT_FAILURE_THRESHOLD} accumulates. */
    static final Duration CIRCUIT_FAILURE_WINDOW = Duration.ofSeconds(120);
    /** How long a provider stays excluded after the circuit opens. */
    static final Duration CIRCUIT_COOLDOWN = Duration.ofSeconds(60);

    /** Per-provider failure timestamps, head-trimmed to the sliding window on each access. */
    private final java.util.concurrent.ConcurrentHashMap<String, java.util.ArrayDeque<Instant>> recentFailures =
            new java.util.concurrent.ConcurrentHashMap<>();
    /** Per-provider time the circuit was last opened. {@code null} entry == closed. */
    private final java.util.concurrent.ConcurrentHashMap<String, Instant> openedAt =
            new java.util.concurrent.ConcurrentHashMap<>();

    @Inject
    public ProviderSelector(NatsConnectionProvider natsProvider,
                            ObjectMapper objectMapper,
                            TicketManager ticketManager,
                            FitPredictor fitPredictor,
                            EngineDriver driver) {
        this.natsProvider = natsProvider;
        this.objectMapper = objectMapper;
        this.ticketManager = ticketManager;
        this.fitPredictor = fitPredictor;
        this.driver = driver != null ? driver : new OllamaDriver(null);
    }

    /**
     * Pick the freest provider for the given model. Returns the picked
     * ProviderState wrapped in Optional; empty when no candidate matches
     * or NATS is unavailable (caller should fall back to its static
     * endpoint in that case).
     *
     * Selection rules (in order):
     * 1. Filter to ready providers
     * 2. Prefer providers with the model already loaded (warm)
     *    - if none warm, fall back to all ready providers (cold-load OK)
     * 3. Among the filtered set, prefer those with a free slot
     *    - if none free, fall back to least-saturated (queue on the smallest queue)
     * 4. Among the survivors, lowest saturationRatio wins; ties broken by
     *    higher maxParallel (more headroom)
     */
    public Optional<ProviderState> pickFor(String modelName) {
        List<ProviderState> all = readState();
        if (all.isEmpty()) return Optional.empty();

        List<ProviderState> ready = all.stream().filter(ProviderState::ready).toList();
        if (ready.isEmpty()) return Optional.empty();

        List<ProviderState> warm = ready.stream()
                .filter(p -> p.hasModelLoaded(modelName))
                .toList();
        List<ProviderState> candidates = warm.isEmpty() ? ready : warm;

        List<ProviderState> free = candidates.stream().filter(ProviderState::hasFreeSlot).toList();
        if (!free.isEmpty()) candidates = free;

        // Smart scoring: combine saturation (state-reported load) with
        // OBSERVED latency from the agent's own recent calls. State can be
        // stale (probe lag, model evicted between probes); observed latency
        // catches false-warm cases automatically — a provider that took 30s
        // last call gets a penalty equivalent to 3 queued agents, so the
        // selector avoids it next time unless the alternative is worse.
        ProviderState pick = candidates.stream()
                .min(Comparator.comparingDouble(this::scoreForRanking)
                        .thenComparing(Comparator.comparingInt(ProviderState::maxParallel).reversed()))
                .orElse(null);

        if (pick == null) return Optional.empty();
        log.debug("Picked provider {} for model {} (sat={}, observedLatencyMs={}, warm={}, free={})",
                pick.name(), modelName, pick.saturationRatio(),
                recentLatency.getOrDefault(pick.endpoint(), 0.0),
                pick.hasModelLoaded(modelName), pick.hasFreeSlot());
        return Optional.of(pick);
    }

    /**
     * v2 selection + claim — slot-based for warm models, headroom-based
     * for cold loads. See {@code kubemoot/docs/scheduler.md} —
     * "Update (2026-05-28): VRAM-headroom tickets".
     *
     * <h3>Why slot + headroom, not headroom alone</h3>
     * The 2026-05-28 v2 first cut treated the ticket as a VRAM
     * footprint reservation, with the call's footprint = the loaded
     * model size (e.g. 27 GiB for qwen3:32b). On a 32 GiB GPU with
     * the model already warm, that left only ~5 GiB of free headroom —
     * not enough for another 27 GiB claim — so every selection rejected
     * and fell back to the static endpoint. v1 behaviour, no bin-pack.
     *
     * The actual cost of a call differs by regime:
     * <ul>
     *   <li><b>Warm</b> (model already loaded): per-call scratch only
     *       (KV cache + activations, ~hundreds of MiB). Multiple
     *       concurrent calls share the loaded weights and serialize at
     *       Ollama's {@code num_parallel} gate. The right constraint
     *       is "do I have a free SLOT?" not "do I have N GiB free?".</li>
     *   <li><b>Cold</b> (model not loaded here): full model footprint
     *       must fit free VRAM to load + run. Headroom-based gate.</li>
     * </ul>
     *
     * So v2.1: each provider passes one of two gates depending on its
     * warm/cold state for the requested model. Slot-count
     * ({@code activeTicketCount < maxParallel}) for warm; VRAM headroom
     * ({@code preTicketHeadroom − activeFootprint ≥ coldLoadFootprint})
     * for cold. Caller passes only the cold-load footprint; the warm
     * scratch is a constant ({@link #WARM_SCRATCH_MIB}).
     *
     * @param modelName            the LLM to serve this call
     * @param coldLoadFootprintMiB MiB needed to load the model from scratch
     *                              (used as the cold-load gate, and as the
     *                              ticket footprint when claiming on a cold
     *                              provider). Caller derives from
     *                              {@code ProviderState.footprintMiBFor(modelName)}
     *                              on any provider that has it loaded, or
     *                              {@code Model.spec.vramMib} CR hint when none does.
     */
    public Optional<Pick> pickAndClaim(String modelName, long coldLoadFootprintMiB) {
        return pickAndClaim(modelName, coldLoadFootprintMiB, 0L);
    }

    /**
     * Phase D overload — also propagates this call's estimated KV-cache
     * footprint so the FitPredictor sees real in-flight KV pressure and
     * concurrent claims accumulate visibly. Passing 0 reproduces v2.2
     * model-weights-only behavior. See {@link KvCacheEstimator}.
     */
    public Optional<Pick> pickAndClaim(String modelName, long coldLoadFootprintMiB,
                                        long thisCallKvCacheMiB) {
        return pickAndClaim(modelName, coldLoadFootprintMiB, thisCallKvCacheMiB, Mode.ANY);
    }

    /**
     * Like {@link #pickAndClaim(String, long, long)}, restricted to providers where
     * the model is already warm (resident or loading) and a slot is free: a claim
     * here costs no load and no eviction. Empty when no provider has the model warm
     * with room.
     */
    public Optional<Pick> pickAndClaimWarm(String modelName, long coldLoadFootprintMiB,
                                           long thisCallKvCacheMiB) {
        return pickAndClaim(modelName, coldLoadFootprintMiB, thisCallKvCacheMiB, Mode.WARM_FREE_SLOT);
    }

    /**
     * Like {@link #pickAndClaim(String, long, long)}, restricted to providers where an
     * in-flight call is loading the model right now: the caller converges on that
     * loading copy and queues there instead of loading a second copy elsewhere.
     */
    public Optional<Pick> pickAndClaimLoading(String modelName, long coldLoadFootprintMiB,
                                              long thisCallKvCacheMiB) {
        return pickAndClaim(modelName, coldLoadFootprintMiB, thisCallKvCacheMiB, Mode.LOADING);
    }

    /** Which ranked candidates a claim may use. */
    enum Mode {
        /** Every feasible candidate, by the weighted cost. */
        ANY,
        /** Warm (resident or loading) with a free slot. */
        WARM_FREE_SLOT,
        /** Being loaded by an in-flight call, not yet resident. */
        LOADING;

        boolean admits(Candidate c, String model, EngineDriver driver) {
            return switch (this) {
                case ANY -> true;
                case WARM_FREE_SLOT -> c.warm() && driver.hasFreeCapacity(c.provider(), c.activeCount());
                case LOADING -> c.warm() && !c.provider().hasModelLoaded(model);
            };
        }
    }

    /**
     * True when at least one known provider has enough usable VRAM to hold a model
     * of {@code footprintMiB}, ignoring what is resident or in flight now. False
     * means no GPU can ever hold it (the model is too large), as opposed to every
     * GPU being busy. Providers that have not published their VRAM total are
     * unknown and never prove the model too large, so an all-unknown list is true.
     */
    public static boolean canEverHold(List<ProviderState> states, long footprintMiB) {
        if (states == null || states.isEmpty() || footprintMiB <= 0) return true;
        boolean anyKnown = false;
        for (ProviderState p : states) {
            long usable = StaticFitPredictor.usableVramMiB(p.totalVramMiB());
            if (usable <= 0) continue;
            anyKnown = true;
            if (usable >= footprintMiB) return true;
        }
        return !anyKnown;
    }

    /** Warm (resident or loading) and the provider can start another call now. */
    boolean warmWithFreeSlot(Candidate c) {
        return Mode.WARM_FREE_SLOT.admits(c, c.provider().name(), driver);
    }

    /** A warm candidate whose copy another call plans to unload is not warm for this call. */
    private boolean plannedForEviction(Candidate c, String model) {
        return c.warm() && ticketManager.plannedEvictionsOn(c.provider().name()).containsKey(model);
    }

    // ---- when no provider has room: queue on a loaded copy, or unload idle models ----

    /**
     * The shortest expected wait for a slot on a loaded (or loading) copy of any of
     * {@code models}, from the in-flight calls on each provider and its recent call
     * latency ({@link PlacementCostModel#expectedWaitSeconds}). Empty when no ready
     * provider has any of them.
     */
    public Optional<PlacementCostModel.QueueOption> queueOption(List<String> models, List<ProviderState> states) {
        long now = System.currentTimeMillis();
        PlacementCostModel.QueueOption best = null;
        for (ProviderState p : states) {
            if (!p.ready() || isCircuitOpen(p.name())) continue;
            for (String m : models) {
                if (!p.hasModelLoaded(m) && !ticketManager.activeModelsOn(p.name()).contains(m)) continue;
                double wait = driver.expectedWaitSeconds(p, elapsedSeconds(p, now), callSeconds(p));
                if (best == null || wait < best.waitSeconds()) {
                    best = new PlacementCostModel.QueueOption(m, p.name(), wait);
                }
            }
        }
        return Optional.ofNullable(best);
    }

    private List<Double> elapsedSeconds(ProviderState p, long nowMs) {
        return ticketManager.inFlightStartsFor(p.name()).stream()
                .map(t -> Math.max(0L, nowMs - t.toEpochMilli()) / 1000.0)
                .toList();
    }

    /** Recent observed call latency on the provider, or the default when none was observed. */
    double callSeconds(ProviderState p) {
        double ms = recentLatency.getOrDefault(p.endpoint(), 0.0);
        return ms > 0 ? ms / 1000.0 : PlacementCostModel.DEFAULT_CALL_SECONDS;
    }

    /**
     * The cheapest plan to unload idle resident models so {@code model} (of
     * {@code footprintMiB}) fits, across ready providers whose usable VRAM can hold
     * it. Models with in-flight work, or already planned for unloading by another
     * call, are never chosen. Empty when no provider can make room.
     */
    public Optional<PlacementCostModel.EvictionPlan> planEviction(String model, long footprintMiB,
                                                                 List<ProviderState> states,
                                                                 java.util.Map<String, ModelDemand> demand) {
        PlacementCostModel.EvictionPlan best = null;
        for (ProviderState p : states) {
            PlacementCostModel.EvictionPlan plan = planOn(p, model, footprintMiB, demand);
            if (plan != null && (best == null || plan.costSeconds() < best.costSeconds())) {
                best = plan;
            }
        }
        return Optional.ofNullable(best);
    }

    private PlacementCostModel.EvictionPlan planOn(ProviderState p, String model, long footprintMiB,
                                                    java.util.Map<String, ModelDemand> demand) {
        long usable = StaticFitPredictor.usableVramMiB(p.totalVramMiB());
        if (!p.ready() || isCircuitOpen(p.name()) || usable < footprintMiB) return null;
        java.util.Map<String, Long> residents = new java.util.HashMap<>(
                p.loadedModelFootprintsMiB() == null ? java.util.Map.of() : p.loadedModelFootprintsMiB());
        ticketManager.residentFootprintsFor(p.name()).forEach((m, fp) -> residents.merge(m, fp, Math::max));
        java.util.Set<String> unavailable = new java.util.HashSet<>(ticketManager.activeModelsOn(p.name()));
        unavailable.addAll(ticketManager.plannedEvictionsOn(p.name()).keySet());
        unavailable.add(model);
        long free = usable - residents.values().stream().mapToLong(Long::longValue).sum()
                - ticketManager.activeFootprintFor(p.name());
        SwitchCostProfile profile = driver.costProfile();
        var victims = EvictionPlanner.chooseVictims(residents, unavailable, demand, free, footprintMiB);
        if (victims.isEmpty() || victims.get().isEmpty()) return null;
        return new PlacementCostModel.EvictionPlan(p.name(), victims.get(),
                PlacementCostModel.evictionCostSeconds(profile, footprintMiB, victims.get()));
    }

    /**
     * Claim the provider in {@code plan} for a cold load of {@code model}, with the
     * planned evictions counted in the ticket budget (see
     * {@link TicketManager#claimWithEvictions}). The caller unloads the victims
     * after a successful claim, then makes the call.
     */
    public Optional<Pick> claimWithEvictions(String model, long footprintMiB, long kvMiB,
                                             PlacementCostModel.EvictionPlan plan, List<ProviderState> states) {
        Optional<ProviderState> provider = states.stream().filter(p -> p.name().equals(plan.provider())).findFirst();
        if (provider.isEmpty()) return Optional.empty();
        java.util.Map<String, Long> evictions = new java.util.HashMap<>();
        plan.victims().forEach(v -> evictions.put(v.model(), v.footprintMiB()));
        ProviderState p = provider.get();
        return ticketManager.claimWithEvictions(p.name(), footprintMiB, kvMiB, p.preTicketHeadroomMiB(), model,
                        evictions, p.loadedModels() == null ? java.util.Set.of() : new java.util.HashSet<>(p.loadedModels()))
                .map(t -> new Pick(p, t, FitScore.yes(0L, "cold load after unloading " + plan.victimModels())));
    }

    /** The lifecycle driver that loads and releases models on providers. */
    public EngineDriver driver() {
        return driver;
    }

    /** Drops the cached provider-state snapshot so the next read is fresh. */
    public void invalidateCache() {
        cache.set(null);
    }

    private Optional<Pick> pickAndClaim(String modelName, long coldLoadFootprintMiB,
                                        long thisCallKvCacheMiB, Mode mode) {
        if (coldLoadFootprintMiB <= 0) return Optional.empty();
        if (thisCallKvCacheMiB < 0) thisCallKvCacheMiB = 0L;
        final long kvForThisCall = thisCallKvCacheMiB;

        for (int attempt = 0; attempt < PICK_RETRY_BUDGET; attempt++) {
            List<ProviderState> states = readState();
            if (states.isEmpty()) return Optional.empty();

            List<Candidate> ranked = rankCandidates(
                    states, modelName, coldLoadFootprintMiB,
                    p -> ticketManager.activeCountFor(p.name()),
                    p -> ticketManager.activeFootprintFor(p.name()),
                    p -> recentLatency.getOrDefault(p.endpoint(), 0.0),
                    this::isCircuitOpen,
                    fitPredictor != null ? fitPredictor : DEFAULT_PREDICTOR,
                    p -> ticketManager.activeKvCacheFor(p.name()),
                    kvForThisCall,
                    p -> ticketManager.activeModelsOn(p.name()),       // cold-start convergence
                    p -> ticketManager.residentFootprintsFor(p.name())); // residency overlay (anti-thrash)
            ranked = ranked.stream()
                    .filter(c -> mode.admits(c, modelName, driver) && !plannedForEviction(c, modelName))
                    .toList();

            if (ranked.isEmpty()) {
                log.debug("No candidate fits for model {} (attempt {}): saturated or no headroom incl. KV {} MiB",
                        modelName, attempt + 1, kvForThisCall);
                return Optional.empty();
            }

            Optional<Pick> pick = claimFirstAvailable(
                    ranked, modelName, coldLoadFootprintMiB, kvForThisCall, attempt);
            if (pick.isPresent()) return pick;
            cache.set(null);
        }
        log.debug("pickAndClaim exhausted {} attempts for model {}", PICK_RETRY_BUDGET, modelName);
        return Optional.empty();
    }

    /**
     * Walk the ranked candidates best-first and return the first one whose
     * ticket claim succeeds, or {@link Optional#empty()} if every candidate
     * loses the race to a concurrent claim. Pure extraction of the inner
     * claim loop of {@link #pickAndClaim}; claim shape and logging unchanged.
     */
    private Optional<Pick> claimFirstAvailable(List<Candidate> ranked, String modelName,
                                               long coldLoadFootprintMiB, long kvForThisCall, int attempt) {
        for (Candidate c : ranked) {
            long claimFootprint = c.warm() ? WARM_SCRATCH_MIB : coldLoadFootprintMiB;
            long headroomBudget = c.provider().preTicketHeadroomMiB();
            // 2026-05-31: stamp modelName on the ticket so other selectors
            // arriving during this call's cold-load see the in-flight load
            // and converge here instead of triggering a redundant cold-load
            // on a different provider. See [[Cold-Start Model-Load Wedge]].
            Optional<Ticket> ticket = ticketManager.claim(
                    c.provider().name(), claimFootprint, kvForThisCall, headroomBudget, modelName);
            if (ticket.isPresent()) {
                log.debug("JIT pick: provider={} model={} warm={} claim={}MiB kv={}MiB slots={}/{} (attempt={})",
                        c.provider().name(), modelName, c.warm(), claimFootprint, kvForThisCall,
                        c.activeCount() + 1, c.provider().maxParallel(), attempt + 1);
                return Optional.of(new Pick(c.provider(), ticket.get(), c.score()));
            }
        }
        return Optional.empty();
    }

    /** Max retries when every ranked candidate loses the race to concurrent claims. */
    static final int PICK_RETRY_BUDGET = 5;

    /**
     * Merge operator-published per-model resident footprints with the residency
     * overlay, taking the per-model MAX so a just-loaded model recorded in the
     * overlay is counted immediately, without double-counting one the probe has
     * already published. Returns the summed resident footprint in MiB. Either map
     * may be null/empty.
     */
    static long mergedResidentMiB(java.util.Map<String, Long> published,
                                  java.util.Map<String, Long> overlay) {
        java.util.Map<String, Long> pub = published == null ? java.util.Map.of() : published;
        java.util.Map<String, Long> ov = overlay == null ? java.util.Map.of() : overlay;
        if (pub.isEmpty() && ov.isEmpty()) return 0L;
        java.util.Set<String> models = new java.util.HashSet<>(pub.keySet());
        models.addAll(ov.keySet());
        long total = 0L;
        for (String m : models) {
            long a = pub.getOrDefault(m, 0L);
            long b = ov.getOrDefault(m, 0L);
            total += Math.max(a, b);
        }
        return total;
    }

    /**
     * Per-call scratch VRAM reservation when the model is already warm
     * on the chosen provider. KV cache + activations for a single
     * concurrent qwen3-class inference are typically a few hundred MiB;
     * 512 is a safe upper bound that doesn't gate practical packing.
     * Used as the ticket's recorded footprint so cold-load math stays
     * consistent when a concurrent caller racing for cold-load headroom
     * sees this ticket's reservation.
     */
    static final long WARM_SCRATCH_MIB = 512;

    /**
     * Candidate provider with the warm/cold and concurrency metadata
     * needed by {@link #pickAndClaim} to issue the right claim shape.
     * Internal carrier for {@link #rankCandidates}.
     */
    record Candidate(ProviderState provider, boolean warm, int activeCount, double latency, FitScore score) {}

    /**
     * The per-provider state accessors {@link #buildFits} reads while evaluating
     * each provider's fit. Bundled into one carrier so the fit loop takes a single
     * parameter instead of five separate function arguments. A null field is
     * tolerated and defaulted inside {@code buildFits}, exactly as the prior
     * per-argument null guards did — behaviour is unchanged.
     */
    record FitAccessors(
            java.util.function.ToIntFunction<ProviderState> activeCount,
            java.util.function.ToDoubleFunction<ProviderState> observedLatency,
            java.util.function.Predicate<String> isCircuitOpen,
            java.util.function.ToLongFunction<ProviderState> activeKvCache,
            java.util.function.Function<ProviderState, java.util.Set<String>> activeModels) {}

    /**
     * Pure selection-ranking helper for {@link #pickAndClaim}, package-private
     * for unit testing. Filters to ready providers that pass the right gate
     * (slot count for warm, VRAM headroom for cold-load), sorts best-fit
     * first, returns the ordered candidate list.
     *
     * Gates:
     * <ul>
     *   <li><b>Warm</b> (model loaded on provider): pass when
     *       {@code activeTicketCount < maxParallel}. Slot semantics —
     *       concurrent calls share the loaded weights.</li>
     *   <li><b>Cold</b> (model not loaded on provider): pass when
     *       {@code preTicketHeadroom − activeFootprint ≥ coldLoadFootprint}.
     *       Cold-load needs the full model size of free VRAM.</li>
     * </ul>
     *
     * Ranking (lowest score wins):
     * <ol>
     *   <li><b>Primary:</b> {@code activeCount / maxParallel} ascending —
     *       fewest in-flight-per-slot wins. Bin-pack-spread: the second
     *       caller sees the first's ticket and naturally picks the other
     *       provider. Emergent balance without static heuristics.</li>
     *   <li><b>Tiebreak 1:</b> warm beats cold at equal saturation —
     *       avoid the cold-load latency tax.</li>
     *   <li><b>Tiebreak 2:</b> lower self-observed latency wins —
     *       a provider that just took 30s gets bumped down even at
     *       equal saturation. Self-correcting against false-warm cases.</li>
     * </ol>
     */
    static List<Candidate> rankCandidates(
            List<ProviderState> states,
            String modelName,
            long coldLoadFootprintMiB,
            java.util.function.ToIntFunction<ProviderState> activeCountFn,
            java.util.function.ToLongFunction<ProviderState> activeFootprintFn,
            java.util.function.ToDoubleFunction<ProviderState> observedLatencyFn,
            java.util.function.Predicate<String> isCircuitOpenFn,
            FitPredictor predictor) {
        return rankCandidates(states, modelName, coldLoadFootprintMiB,
                activeCountFn, activeFootprintFn, observedLatencyFn,
                isCircuitOpenFn, predictor, p -> 0L, 0L);
    }

    /**
     * Phase D overload — also threads {@code activeKvCacheFn} (sum of
     * in-flight ticket KV reservations) and {@code thisCallKvCacheMiB}
     * (this call's estimated KV-cache need) into {@link FitInputs} so
     * the predictor's cold-load gate factors in real KV-cache VRAM
     * pressure. Old callers that pass {@code 0L} for both reproduce
     * the v2.2 model-weights-only behavior.
     */
    static List<Candidate> rankCandidates(
            List<ProviderState> states,
            String modelName,
            long coldLoadFootprintMiB,
            java.util.function.ToIntFunction<ProviderState> activeCountFn,
            java.util.function.ToLongFunction<ProviderState> activeFootprintFn,
            java.util.function.ToDoubleFunction<ProviderState> observedLatencyFn,
            java.util.function.Predicate<String> isCircuitOpenFn,
            FitPredictor predictor,
            java.util.function.ToLongFunction<ProviderState> activeKvCacheFn,
            long thisCallKvCacheMiB) {
        return rankCandidates(states, modelName, coldLoadFootprintMiB,
                activeCountFn, activeFootprintFn, observedLatencyFn,
                isCircuitOpenFn, predictor, activeKvCacheFn, thisCallKvCacheMiB,
                p -> java.util.Set.of(), p -> java.util.Map.of());
    }

    /**
     * Cold-start convergence overload — threads {@code activeModelsFn} so
     * the FitPredictor and the score function can recognise providers
     * that are currently cold-loading the target model (via in-flight
     * ticket inspection). A second selector arriving during the
     * cold-load window treats the loading provider as warm-equivalent
     * and converges on it, avoiding the redundant cold-load on another
     * provider that drove the 5× slowdown in [[Cold-Start Model-Load Wedge]].
     */
    static List<Candidate> rankCandidates(
            List<ProviderState> states,
            String modelName,
            long coldLoadFootprintMiB,
            java.util.function.ToIntFunction<ProviderState> activeCountFn,
            java.util.function.ToLongFunction<ProviderState> activeFootprintFn,
            java.util.function.ToDoubleFunction<ProviderState> observedLatencyFn,
            java.util.function.Predicate<String> isCircuitOpenFn,
            FitPredictor predictor,
            java.util.function.ToLongFunction<ProviderState> activeKvCacheFn,
            long thisCallKvCacheMiB,
            java.util.function.Function<ProviderState, java.util.Set<String>> activeModelsFn,
            java.util.function.Function<ProviderState, java.util.Map<String, Long>> residentOverlayFn) {
        if (states == null || coldLoadFootprintMiB <= 0) return List.of();
        java.util.function.Function<ProviderState, java.util.Map<String, Long>> overlayFn =
                residentOverlayFn == null ? p -> java.util.Map.of() : residentOverlayFn;

        List<Candidate> fits = buildFits(
                states, modelName, coldLoadFootprintMiB, predictor, thisCallKvCacheMiB,
                new FitAccessors(activeCountFn, observedLatencyFn, isCircuitOpenFn,
                        activeKvCacheFn, activeModelsFn));
        if (fits.isEmpty()) return List.of();

        // Weighted-cost placement (lower is better; pick argmin). No tiers - warm,
        // cold-onto-free, and cold-with-eviction are regions that fall out of the
        // summed weights. See [[JIT Scheduler Locality Algorithm]].
        //   contention           in-flight calls competing for this GPU's slots
        //   + reliabilityPenalty learned success-rate (v2 LearningFitPredictor)
        //   + loadCost           0 if warm (already resident); a model load otherwise
        //   + evictCost          0 if warm or cold-onto-free; large if a load must
        //                        evict resident models; near-forbidden if it would
        //                        evict a card with in-flight (live) work
        fits.sort(Comparator
                .<Candidate>comparingDouble(c -> placementCost(c, coldLoadFootprintMiB, activeFootprintFn, overlayFn))
                .thenComparingDouble(Candidate::latency));
        return fits;
    }

    /**
     * Build the feasible-candidate list for {@link #rankCandidates}: for each
     * provider, evaluate the {@link FitPredictor} gate and keep only those that
     * {@code canServe}. Pure extraction of the per-provider fit loop; behaviour
     * and ordering are identical to the inline form.
     */
    private static List<Candidate> buildFits(
            List<ProviderState> states,
            String modelName,
            long coldLoadFootprintMiB,
            FitPredictor predictor,
            long thisCallKvCacheMiB,
            FitAccessors accessors) {
        java.util.function.Predicate<String> circuitOpen =
                accessors.isCircuitOpen() == null ? n -> false : accessors.isCircuitOpen();
        FitPredictor fit = (predictor == null) ? DEFAULT_PREDICTOR : predictor;
        java.util.function.ToLongFunction<ProviderState> kvSum =
                accessors.activeKvCache() == null ? p -> 0L : accessors.activeKvCache();
        java.util.function.Function<ProviderState, java.util.Set<String>> modelsFn =
                accessors.activeModels() == null ? p -> java.util.Set.of() : accessors.activeModels();

        List<Candidate> fits = new ArrayList<>();
        for (ProviderState p : states) {
            int active = accessors.activeCount().applyAsInt(p);
            double latency = accessors.observedLatency().applyAsDouble(p);
            FitInputs inputs = new FitInputs(
                    p, modelName,
                    active,
                    kvSum.applyAsLong(p),               // Phase D: actual in-flight KV reservations
                    coldLoadFootprintMiB,
                    thisCallKvCacheMiB,                  // Phase D: this call's KV estimate
                    latency,
                    circuitOpen.test(p.name()),
                    modelsFn.apply(p));                  // 2026-05-31: in-flight cold-loads
            FitScore score = fit.predict(inputs);
            if (!score.canServe()) continue;
            // warm = truly-warm OR an active ticket is loading this model
            // (in-flight cold-load that this selector should converge on).
            boolean warm = inputs.modelWarmOrLoading();
            fits.add(new Candidate(p, warm, active, latency, score));
        }
        return fits;
    }

    /**
     * Weighted placement cost for a single candidate (lower is better). Pure
     * extraction of the {@link #rankCandidates} sort comparator's primary key;
     * the summed terms and their weights are unchanged.
     */
    private static double placementCost(
            Candidate c,
            long coldLoadFootprintMiB,
            java.util.function.ToLongFunction<ProviderState> activeFootprintFn,
            java.util.function.Function<ProviderState, java.util.Map<String, Long>> overlayFn) {
        ProviderState p = c.provider();
        long usable = StaticFitPredictor.usableVramMiB(p.totalVramMiB());
        // Everything that takes up room, freshest-first:
        //   resident = per-model MAX(operator-published /api/ps, residency
        //              overlay). The overlay is recorded the instant a model
        //              runs on a provider, so a just-loaded model is visible
        //              here without waiting for the ~30s probe - the fix for
        //              the 2026-06-11 thrash where a stale-free 5090 let 8B
        //              cold-load onto the resident 32B. MAX (not sum) avoids
        //              double-counting a model the probe already published.
        //   inFlight = in-flight per-call ticket claims (a concurrent load
        //              not yet resident).
        long resident = mergedResidentMiB(p.loadedModelFootprintsMiB(), overlayFn.apply(p));
        long publishedResident = p.loadedFootprintSumMiB();
        long inFlight = activeFootprintFn == null ? 0L : activeFootprintFn.applyAsLong(p);
        long free = Math.max(0L, usable - resident - inFlight);
        // Best-fit (best-fit-decreasing bin-packing): prefer the card
        // whose usable VRAM most tightly fits this model, so a big GPU
        // is reserved for a big model instead of being squatted by a
        // small one. Without this, a small model happily takes a big
        // card's free room (more free = looks attractive), then a model
        // that ONLY fits the big card has to evict it - the 2026-06-11
        // thrash. excess/occupancy is dimensionless so it scales across
        // GPU sizes; applies warm AND cold so a model warm on a too-big
        // card is nudged back toward its right-sized home.
        double bestFit = coldLoadFootprintMiB > 0
                ? BEST_FIT_WEIGHT * Math.max(0L, usable - coldLoadFootprintMiB)
                        / coldLoadFootprintMiB
                : 0.0;
        double contention = (double) c.activeCount()
                / Math.max(p.maxParallel(), 1);
        double reliabilityPenalty = (1.0 - c.score().successProbability())
                * RELIABILITY_PENALTY_WEIGHT;
        // Spill weight: a card whose resident set already exceeds usable
        // VRAM is running partly on CPU - glacial. Weight it heavily so a
        // healthy card wins even if busier; feasibility still keeps the
        // spilling card as a last-resort fallback (it is not excluded).
        double spillCost = (usable > 0 && publishedResident > usable) ? SPILL_PENALTY : 0.0;
        double loadCost = c.warm() ? 0.0 : COLD_LOAD_PENALTY;
        double evictCost = c.warm() ? 0.0 : evictionCost(c, coldLoadFootprintMiB, free);
        return contention + reliabilityPenalty + spillCost + loadCost + evictCost + bestFit;
    }

    /**
     * Eviction-cost term for a COLD candidate. Zero when the cold load fits free
     * VRAM; {@link #EVICTION_LIVE_PENALTY} when the card has in-flight work that
     * a load would disrupt; otherwise a scaled {@link #EVICTION_IDLE_BASE}. Pure
     * extraction of the inline cold-eviction branch; values are unchanged.
     */
    private static double evictionCost(Candidate c, long coldLoadFootprintMiB, long free) {
        if (coldLoadFootprintMiB <= free) {
            return 0.0;
        }
        if (c.activeCount() > 0) {
            // The card has in-flight work; loading here evicts a
            // LIVE model and forces its reload. Near-forbidden -
            // this is the term that stops the 2026-06-11 thrash
            // (8B migrating onto the 32B-busy 5090).
            return EVICTION_LIVE_PENALTY;
        }
        // Idle resident models: eviction allowed but costly
        // (victims reload later). Scale by how much must go.
        double evictFrac = Math.min(1.0,
                (coldLoadFootprintMiB - free)
                        / (double) Math.max(coldLoadFootprintMiB, 1L));
        return EVICTION_IDLE_BASE * (1.0 + evictFrac);
    }

    /**
     * Load-cost weight for a cold candidate (model not resident, no in-flight
     * load). A model load is roughly several queued calls of wall-clock time, so
     * 5.0 means "cold and idle" only beats a warm card once that warm card has
     * ~5 calls queued ahead - locality wins by cost, not by rule. Applies to any
     * cold load (onto free room or with eviction). See [[JIT Scheduler Locality Algorithm]].
     */
    static final double COLD_LOAD_PENALTY = 5.0;

    /**
     * Penalty for a card whose resident set already exceeds usable VRAM - it is
     * spilling to CPU and serves glacially. 20.0 keeps a spilling card below any
     * healthy alternative (even a cold load, weight 5) while leaving it feasible
     * as a last resort. See [[JIT Scheduler Locality Algorithm]].
     */
    static final double SPILL_PENALTY = 20.0;

    /**
     * Best-fit weight. Scales {@code excess/occupancy} (how much bigger the card
     * is than the model needs) into the cost, so a model prefers the SMALLEST card
     * that fits it. Reserves big GPUs for big models and stops a small model from
     * squatting a big card's free room (then being evicted by a model that only
     * fits there).
     *
     * <p>Sized to DOMINATE contention, deliberately. Queueing on the right-fit card
     * must always beat migrating to a wrong-fit card, because migration costs a
     * full model load PLUS a future eviction while queueing is only wait;
     * contention should only break ties among equally-good-fit cards, never push a
     * model off the card it belongs on. With {@code num_parallel=1} a provider's
     * contention climbs to ~the subcommittee size (8-15) under fan-out, so the
     * best-fit GAP between two cards must exceed that. At 25.0 the 5090-vs-4090 gap
     * for an 8B is ~30 (= 25 x (excess5090 - excess4090)/occupancy), comfortably
     * above any realistic fan-out (a weaker 8.0 gave a ~9.5 gap that a burst beat,
     * 2026-06-11). Still far below {@code EVICTION_LIVE_PENALTY}, so safety wins.
     * The standard best-fit-decreasing heuristic; scales to N heterogeneous GPUs.
     * See [[JIT Scheduler Locality Algorithm]].
     */
    static final double BEST_FIT_WEIGHT = 25.0;

    /**
     * Eviction-cost weight when a cold load must evict IDLE resident models to
     * fit (no in-flight work on the card). Eviction is expensive: the victims
     * reload later. 50.0 (×(1+evictFrac)) keeps it far above any realistic
     * contention or load weight, so evicting an idle card is chosen only when
     * every warm/cold-onto-free option is dramatically worse. See [[JIT Scheduler Locality Algorithm]].
     */
    static final double EVICTION_IDLE_BASE = 50.0;

    /**
     * Eviction-cost weight when a cold load would evict a card that has IN-FLIGHT
     * (live) work - disrupting running calls and forcing their reload. Near-
     * forbidden: 1000.0 dwarfs every other term, so a live card is evicted only
     * as an absolute last resort when nothing else can serve M. This is the term
     * that stops the 2026-06-11 thrash (8B migrating onto the 32B-busy 5090).
     */
    static final double EVICTION_LIVE_PENALTY = 1000.0;

    /**
     * Weight applied to learned reliability in the composite ranking
     * score. {@code (1 - SR) × WEIGHT} is added to the saturation
     * ratio. 0.5 means a 0% SR provider scores 0.5 worse than a 100%
     * SR provider at the same load — comparable to one extra ticket
     * on a maxParallel=2 provider.
     */
    static final double RELIABILITY_PENALTY_WEIGHT = 0.5;

    /**
     * Ranking score for selection — LOWER is better (so min wins).
     * Combines state-reported saturation with self-observed latency:
     * {@code saturationRatio + (recentLatencyMs/1000 * LATENCY_PENALTY_PER_SECOND)}.
     * A 30s observed latency adds 3.0 to the score, equivalent to a
     * provider with 3 agents already queued.
     */
    double scoreForRanking(ProviderState state) {
        double latencyMs = recentLatency.getOrDefault(state.endpoint(), 0.0);
        double latencyPenalty = (latencyMs / 1000.0) * LATENCY_PENALTY_PER_SECOND;
        return state.saturationRatio() + latencyPenalty;
    }

    /**
     * Record a failed inference call against the named provider. After
     * {@link #CIRCUIT_FAILURE_THRESHOLD} failures within
     * {@link #CIRCUIT_FAILURE_WINDOW}, the circuit opens for
     * {@link #CIRCUIT_COOLDOWN} — the provider is excluded from
     * {@link #rankCandidates} entirely during that window.
     *
     * Caller is {@code ChatService}'s finally block — fire on any path
     * that didn't produce a usable response (timeout, 5xx, exception,
     * ToolCallFailure with infrastructure cause). Don't fire for
     * application-level signals like stand_aside.
     */
    public void recordFailure(String provider) {
        if (provider == null || provider.isEmpty()) return;
        Instant now = Instant.now();
        java.util.ArrayDeque<Instant> window = recentFailures.computeIfAbsent(
                provider, k -> new java.util.ArrayDeque<>());
        synchronized (window) {
            // Trim out-of-window entries first.
            Instant cutoff = now.minus(CIRCUIT_FAILURE_WINDOW);
            while (!window.isEmpty() && window.peekFirst().isBefore(cutoff)) {
                window.pollFirst();
            }
            window.addLast(now);
            if (window.size() >= CIRCUIT_FAILURE_THRESHOLD) {
                openedAt.put(provider, now);
                log.warn("Circuit OPEN for provider {} after {} failures in {}s; excluding for {}s",
                        provider, window.size(), CIRCUIT_FAILURE_WINDOW.toSeconds(),
                        CIRCUIT_COOLDOWN.toSeconds());
            }
        }
    }

    /**
     * Record a successful inference call. Closes the circuit (clears the
     * open timestamp + the failure window) so a recovered provider can
     * be picked again immediately. Called by the half-open probe path:
     * if the provider was open, the next pick that lands there is the
     * probe; success returns it to normal rotation.
     */
    public void recordSuccess(String provider) {
        if (provider == null || provider.isEmpty()) return;
        if (openedAt.remove(provider) != null) {
            log.info("Circuit CLOSED for provider {} after successful probe", provider);
        }
        java.util.ArrayDeque<Instant> window = recentFailures.get(provider);
        if (window != null) {
            synchronized (window) { window.clear(); }
        }
    }

    /**
     * Returns true if the named provider's circuit is currently open
     * (excluded from selection). False after the cooldown elapses, at
     * which point the provider becomes pickable again as a half-open
     * probe candidate.
     */
    public boolean isCircuitOpen(String provider) {
        if (provider == null || provider.isEmpty()) return false;
        Instant ot = openedAt.get(provider);
        if (ot == null) return false;
        // Open only while still inside the cooldown window. Once the cooldown has
        // elapsed the open record is left in place (recordSuccess clears it on the
        // next successful probe), so a still-broken provider re-opens immediately
        // on the next failure - the failure window is intentionally not cleared
        // until success.
        return !Instant.now().isAfter(ot.plus(CIRCUIT_COOLDOWN));
    }

    /** Test-only accessor for the per-provider failure window. */
    java.util.Deque<Instant> failureWindowForTest(String provider) {
        return recentFailures.get(provider);
    }

    /** Test-only accessor for the per-provider open-at timestamp. */
    Instant openedAtForTest(String provider) {
        return openedAt.get(provider);
    }

    /**
     * Feed an inference-call outcome to the injected {@link FitPredictor}
     * so it can learn per-(provider, model) success rate and latency.
     * Called by ChatService's finally block alongside the existing
     * {@link #recordSuccess}/{@link #recordFailure} circuit-breaker hooks.
     * No-op when no predictor is injected or when the call had no
     * resolved provider (static-fallback path).
     */
    public void recordOutcome(String provider, String modelName, long latencyMs, boolean success) {
        FitPredictor fp = (fitPredictor == null) ? DEFAULT_PREDICTOR : fitPredictor;
        fp.recordOutcome(provider, modelName, latencyMs, success);
    }

    /**
     * Record the observed end-to-end latency for an inference call against
     * a provider endpoint. Called by ChatService after each successful
     * call. EMA-smoothed so a single outlier doesn't dominate ranking.
     *
     * Used by the next {@link #pickFor} call: if observed latency for
     * endpoint X is significantly higher than peers (e.g., X cold-loaded
     * a 32B model while peers were already warm), X gets penalised and
     * subsequent picks prefer alternatives — even when MP state still
     * claims X is warm. Self-correcting against state staleness.
     */
    public void recordObservedLatency(String endpoint, long latencyMs) {
        if (endpoint == null || endpoint.isEmpty() || latencyMs <= 0) return;
        recentLatency.merge(endpoint, (double) latencyMs,
                (prev, sample) -> prev * (1.0 - LATENCY_EMA_ALPHA) + sample * LATENCY_EMA_ALPHA);
        log.debug("Observed latency for {}: {}ms (EMA now {}ms)",
                endpoint, latencyMs, recentLatency.get(endpoint));
    }

    /** Test-only accessor for the latency map. */
    java.util.Map<String, Double> latencyMapForTest() {
        return recentLatency;
    }

    /**
     * Read all provider state snapshots from NATS KV, honoring the short
     * in-memory cache. Returns empty list on any failure (NATS down,
     * bucket missing, deserialisation error) — caller handles via Optional.
     *
     * Public so callers in other packages (notably ChatService) can look
     * up a provider's endpoint by name when recording observed latency.
     */
    public List<ProviderState> readState() {
        CachedSnapshot cached = cache.get();
        if (cached != null && cached.isFresh()) {
            return cached.states();
        }
        Connection conn = natsProvider.isAvailable() ? natsProvider.getConnection() : null;
        if (conn == null) {
            lastReadError = "NATS unavailable";
            return List.of();
        }

        try {
            KeyValue kv = conn.keyValue(STATE_BUCKET);
            List<ProviderState> states = new ArrayList<>();
            for (String key : ai.kubemoot.agent.nats.KvKeys.list(conn, STATE_BUCKET)) {
                parseStateInto(kv, key, states);
            }
            lastReadError = null;
            cache.set(new CachedSnapshot(states, Instant.now()));
            return states;
        } catch (Exception e) {
            // NATS answered with an error: callers get no state (and fall back),
            // but the failure is reported and kept apart from an empty bucket.
            lastReadError = e.toString();
            ai.kubemoot.agent.nats.KvKeys.warnReadFailure(log, STATE_BUCKET, "provider state", e);
            return List.of();
        }
    }

    /**
     * Read, parse, and stale-filter a single KV entry, appending the resulting
     * {@link ProviderState} to {@code states} when it is fresh and readable.
     * Unreadable entries are skipped (logged at debug). Pure extraction of the
     * per-key body of {@link #readState}'s loop; behaviour is unchanged.
     */
    private void parseStateInto(KeyValue kv, String key, List<ProviderState> states) {
        try {
            var entry = kv.get(key);
            if (entry == null || entry.getValue() == null) return;
            // readTree + manual mapping, NOT readValue(.., ProviderState.class):
            // ProviderState is a record, and Jackson record deserialisation
            // fails SILENTLY in GraalVM native (the agents run native), which
            // made readState return empty → JIT selection never engaged →
            // every agent fell back to its static endpoint. See
            // [[project_jit_native_deser_bug]]. readTree works in native.
            ProviderState ps = fromJson(objectMapper.readTree(entry.getValue()));
            // Ignore STALE entries: a provider whose lastProbedAt is far older
            // than the operator's probe cycle is an orphan (e.g. a deleted
            // ModelProvider CR whose KV state was never reaped, or a test
            // fixture). Ranking it pollutes placement - it adds a phantom GPU,
            // splits residency/tickets across a duplicate endpoint, and feeds
            // stale footprints. Observed 2026-06-12: a 25h-old
            // "smoke-test-provider" (no CR) duplicated the 5090 in the scheduler.
            if (isStaleState(ps.lastProbedAt())) {
                log.warn("Ignoring stale provider state '{}' (lastProbedAt={} > {} old) - orphaned KV entry, no live ModelProvider",
                        ps.name(), ps.lastProbedAt(), STATE_STALE_AFTER);
                return;
            }
            states.add(ps);
        } catch (Exception e) {
            log.debug("Skipping unreadable provider state for key {}: {}", key, e.getMessage());
        }
    }

    /**
     * Build a {@link ProviderState} from the operator-published JSON via
     * manual tree navigation. Native-safe (no record reflection). Field
     * names mirror the operator's ProviderState struct json tags in
     * modelprovider_controller.go. {@code path(...)} returns MissingNode for
     * absent keys, so {@code asText/asInt/asBoolean} fall back to the
     * supplied default — tolerant of partial/older documents.
     */
    static ProviderState fromJson(JsonNode n) {
        List<String> loaded = new ArrayList<>();
        JsonNode lm = n.path("loadedModels");
        if (lm.isArray()) {
            lm.forEach(m -> loaded.add(m.asText()));
        }
        // v2 fields — both optional. fromJson returns defaults (0, empty
        // map) when the operator hasn't published them yet. ProviderState
        // returns 0 headroom in that case so the selector falls back to
        // its static endpoint rather than over-committing on phantom VRAM.
        java.util.Map<String, Long> footprints = longMapField(n, "loadedModelFootprintsMiB");
        // v3 field: available-model on-disk sizes for cold-start footprint estimation.
        // Absent in older operator versions - default to empty map (graceful degradation).
        java.util.Map<String, Long> availFootprints = longMapField(n, "availableModelFootprintsMiB");
        return new ProviderState(
                n.path("name").asText(""),
                n.path("endpoint").asText(""),
                n.path("maxParallel").asInt(0),
                n.path("activeCount").asInt(0),
                n.path("queueDepth").asInt(0),
                loaded,
                n.path("ready").asBoolean(false),
                n.path("lastProbedAt").asText(""),
                n.path("totalVramMiB").asLong(0L),
                footprints,
                availFootprints
        );
    }

    /**
     * Parse a JSON object field of {@code {model: sizeMiB}} into a string-to-long
     * map, returning an empty (mutable) map when the field is absent or not an
     * object. Pure extraction of the duplicated footprint-map parse in
     * {@link #fromJson}; absent values default to {@code 0L} as before.
     */
    private static java.util.Map<String, Long> longMapField(JsonNode n, String field) {
        java.util.Map<String, Long> out = new java.util.HashMap<>();
        JsonNode node = n.path(field);
        if (node.isObject()) {
            node.fields().forEachRemaining(e -> out.put(e.getKey(), e.getValue().asLong(0L)));
        }
        return out;
    }

    /** Force the cache to refresh on next call. Test-only; not part of normal use. */
    void invalidateCacheForTest() {
        cache.set(null);
    }

    private record CachedSnapshot(List<ProviderState> states, Instant fetchedAt) {
        boolean isFresh() {
            return Instant.now().isBefore(fetchedAt.plus(CACHE_TTL));
        }
    }
}
