package ai.kubemoot.agent.provider;

import ai.kubemoot.agent.provider.CallPlanner.Placement;
import ai.kubemoot.agent.provider.CallPlanner.PlacementRequest;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Map;
import java.util.Optional;

/**
 * The hand-written placement policy.
 *
 * <p>Placement order, from starting now to queueing:</p>
 * <ol>
 *   <li>the preferred model warm (resident or loading) with a free slot;</li>
 *   <li>an in-tolerance candidate warm with a free slot;</li>
 *   <li>the preferred model, then each in-tolerance candidate, loaded onto free
 *       memory when no copy of it is resident or loading anywhere;</li>
 *   <li>queue: join an in-tolerance candidate another call is loading, then the
 *       preferred model under the weighted cost (queue on a warm or loading copy,
 *       or a second copy when the cost says so).</li>
 * </ol>
 * <p>When none of these has room, {@link PlacementCostModel#decide} weighs queueing
 * for a loaded copy against unloading idle models, and the call either unloads and
 * loads, or returns empty so the caller waits for capacity.</p>
 *
 * <p>Two rules meet at a copy that is still loading. Convergence: step 3 never starts
 * a load of a model while a usable copy of it is resident or loading, so agents
 * selected together share one load; when two calls start the same load at once, the
 * ticket written first keeps it and the other joins. Step 3 also admits a warm copy
 * with a free slot, so a call that lost such a race joins the new copy with its slot
 * counted. Slot check: every claim that joins a copy for a free slot re-counts the
 * slots after its ticket is written, ordered by KV revision, so no more claimers keep
 * a loading copy than it has slots (steps 1 to 3). A claimer that loses the slot does
 * not load the same model elsewhere (convergence), and does not queue while another
 * in-tolerance model can start now (step 2 or 3). Queueing on the loading copy is the
 * last resort, taken only when no in-tolerance model can start without it (step 4).</p>
 */
public class AlgorithmPlacement implements PlacementPolicy {

    private static final Logger log = LoggerFactory.getLogger(AlgorithmPlacement.class);

    private final ProviderSelector selector;
    private final TicketManager tickets;
    private final DemandBoard demand;

    /** {@code demand} may be null: eviction then orders victims without forecasts. */
    public AlgorithmPlacement(ProviderSelector selector, TicketManager tickets, DemandBoard demand) {
        this.selector = selector;
        this.tickets = tickets;
        this.demand = demand;
    }

    @Override
    public PlacementMode mode() {
        return PlacementMode.ALGORITHM;
    }

    @Override
    public Optional<Placement> place(PlacementRequest r) {
        Optional<Placement> quick = placeWithoutUnloading(r);
        return quick.isPresent() || !r.mayUnload() ? quick : decideWhenFull(r);
    }

    private Optional<Placement> placeWithoutUnloading(PlacementRequest r) {
        return claim(r, r.preferred(), ProviderSelector.Mode.WARM_FREE_SLOT)
                .or(() -> claimFirst(r, r.alternatives(), ProviderSelector.Mode.WARM_FREE_SLOT))
                .or(() -> claimFirst(r, r.acceptable(), ProviderSelector.Mode.ROOM))
                .or(() -> claimFirst(r, r.alternatives(), ProviderSelector.Mode.LOADING))
                .or(() -> claim(r, r.preferred(), ProviderSelector.Mode.ANY));
    }

    /** The first of {@code models}, in order, that {@code mode} places. */
    private Optional<Placement> claimFirst(PlacementRequest r, List<String> models, ProviderSelector.Mode mode) {
        for (String model : models) {
            Optional<Placement> p = claim(r, model, mode);
            if (p.isPresent()) {
                return p;
            }
        }
        return Optional.empty();
    }

    private Optional<Placement> claim(PlacementRequest r, String model, ProviderSelector.Mode mode) {
        long fp = r.footprint().applyAsLong(model);
        if (fp <= 0) {
            return Optional.empty();
        }
        long kv = r.kv().applyAsLong(model);
        Optional<Pick> pick = switch (mode) {
            case WARM_FREE_SLOT -> selector.pickAndClaimWarm(model, fp, kv, r.promptTokens());
            case LOADING -> selector.pickAndClaimLoading(model, fp, kv, r.promptTokens());
            case ROOM -> selector.pickAndClaimRoom(model, fp, kv, r.promptTokens());
            case ANY -> selector.pickAndClaim(model, fp, kv, r.promptTokens());
            // The algorithm never loads a copy while one is warm elsewhere (convergence).
            case COLD -> Optional.empty();
        };
        return pick.map(p -> new Placement(p, model, fp, List.of()));
    }

    /** No provider has room: queue for a loaded copy, unload idle models, or wait for memory. */
    private Optional<Placement> decideWhenFull(PlacementRequest r) {
        long fp = r.footprint().applyAsLong(r.preferred());
        if (fp <= 0 || r.states().isEmpty()) {
            return Optional.empty();
        }
        Map<String, ModelDemand> now = demand == null ? Map.of() : demand.snapshot();
        var decision = PlacementCostModel.decide(
                selector.queueOption(r.acceptable(), r.states(), r.promptTokens()),
                selector.planEviction(r.preferred(), fp, r.states(), now, r.promptTokens()));
        log.info("No accelerator has room for {}: {} (queue={}, eviction={})", r.preferred(), decision.action(),
                decision.queue().map(q -> q.model() + "@" + q.provider() + " " + Math.round(q.waitSeconds()) + "s").orElse("none"),
                decision.eviction().map(e -> e.victimModels() + "@" + e.provider() + " " + Math.round(e.costSeconds()) + "s").orElse("none"));
        if (decision.action() != PlacementCostModel.Action.EVICT) {
            return Optional.empty();
        }
        return evictAndClaim(r, fp, decision.eviction().orElseThrow());
    }

    private Optional<Placement> evictAndClaim(PlacementRequest r, long fp, PlacementCostModel.EvictionPlan plan) {
        Optional<Pick> pick = selector.claimWithEvictions(r.preferred(), fp, r.kv().applyAsLong(r.preferred()),
                plan, r.states());
        if (pick.isEmpty()) {
            return Optional.empty();
        }
        ProviderState provider = pick.get().provider();
        EngineDriver driver = selector.driver();
        for (String victim : plan.victimModels()) {
            if (!driver.release(provider.endpoint(), victim)) {
                log.warn("{} did not confirm releasing {}", provider.name(), victim);
            }
            tickets.clearResidency(plan.provider(), victim);
        }
        selector.invalidateCache();
        log.info("Unloaded {} on {} to load {}", plan.victimModels(), plan.provider(), r.preferred());
        return Optional.of(new Placement(pick.get(), r.preferred(), fp, plan.victimModels()));
    }
}
