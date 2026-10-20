package ai.kubemoot.agent.modelserver;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.ScheduledFuture;
import java.util.concurrent.TimeUnit;
import java.util.function.Supplier;
import java.util.function.ToLongFunction;

/**
 * Places a mulling call: which model, which server, and what to do when no
 * server has room. Also plans an agent's first call at the moment the
 * coordinator selects it, so a needed load overlaps the agent's triage.
 *
 * <p>The decision itself belongs to the configured {@link PlacementPolicy}
 * ({@link AlgorithmPlacement} by default); this class holds plans made at
 * selection and hands them to the first call.</p>
 *
 * <p>Nothing here loads a model without a selected agent behind it: a plan is made
 * only from {@link #commit}, which the discussion subscriber calls when this agent
 * is selected for a live thread. Demand forecasts only order victims.</p>
 */
@ApplicationScoped
public class CallPlanner {

    private static final Logger log = LoggerFactory.getLogger(CallPlanner.class);

    /** How long a plan made at selection holds its claim when the agent never calls. */
    static final Duration HOLD_TTL = DemandBoard.INTENT_TTL;

    private final ServerSelector selector;
    private final TicketManager tickets;
    private final DemandBoard demand;
    private final PlacementPolicy policy;
    private final Map<String, Held> held = new ConcurrentHashMap<>();
    /**
     * Threads whose plan is no longer wanted (first call started, agent stood aside,
     * thread ended), with the time it happened, so a plan that finishes late is
     * dropped at once instead of holding a claim until {@link #HOLD_TTL}.
     */
    private final Map<String, Long> settled = new ConcurrentHashMap<>();
    private final ScheduledExecutorService timers = Executors.newSingleThreadScheduledExecutor(r -> {
        Thread t = new Thread(r, "call-planner-hold-expiry");
        t.setDaemon(true);
        return t;
    });

    /**
     * What a call needs placed. {@code footprint} and {@code kv} give MiB per model
     * name; {@code promptTokens} is the prompt's estimated size, which the chosen
     * server's context must hold (zero when unknown). {@code mayUnload} false
     * means the call never unloads idle models to make room: it is placed only
     * where it fits now, or not at all.
     */
    public record PlacementRequest(String preferred, List<String> alternatives, List<ServerState> states,
                                   ToLongFunction<String> footprint, ToLongFunction<String> kv,
                                   long promptTokens, boolean mayUnload) {
        /** A request that may unload idle models when nothing fits. */
        public PlacementRequest(String preferred, List<String> alternatives, List<ServerState> states,
                                ToLongFunction<String> footprint, ToLongFunction<String> kv, long promptTokens) {
            this(preferred, alternatives, states, footprint, kv, promptTokens, true);
        }

        /** A request whose prompt size is unknown. */
        public PlacementRequest(String preferred, List<String> alternatives, List<ServerState> states,
                                ToLongFunction<String> footprint, ToLongFunction<String> kv) {
            this(preferred, alternatives, states, footprint, kv, 0L);
        }

        List<String> acceptable() {
            List<String> all = new ArrayList<>();
            all.add(preferred);
            all.addAll(alternatives);
            return all;
        }
    }

    /** A claimed placement. {@code evicted} lists the models unloaded to make room. */
    public record Placement(Pick pick, String model, long footprintMiB, List<String> evicted) {
        /**
         * True when the placement joined a busy copy without a counted call slot (a
         * fallback queue), as opposed to a slot it holds or a load it starts.
         */
        public boolean queued() {
            return pick.queued();
        }

        /** True when the model is not resident on the chosen server yet. */
        public boolean coldLoad() {
            return !pick.server().hasModelLoaded(model);
        }
    }

    private record Held(Placement placement, ScheduledFuture<?> expiry) {}

    @Inject
    public CallPlanner(ServerSelector selector, TicketManager tickets, DemandBoard demand, PlacementPolicy policy) {
        this.selector = selector;
        this.tickets = tickets;
        this.demand = demand;
        this.policy = policy;
    }

    /** A planner that places with {@link AlgorithmPlacement}. */
    public CallPlanner(ServerSelector selector, TicketManager tickets, DemandBoard demand) {
        this(selector, tickets, demand, new AlgorithmPlacement(selector, tickets, demand));
    }

    /** One placement attempt; empty when no server has room and waiting is the better choice. */
    public Optional<Placement> place(PlacementRequest r) {
        return policy.place(r);
    }

    /** The placement mode in use. */
    public PlacementMode mode() {
        return policy.mode();
    }

    // ---- planning at selection ----

    /**
     * The agent was selected for {@code threadId}: publish its intent for
     * {@code candidates}, record the selection for prediction, and plan its first
     * call now. A plan with a claim is held for the first call (or released when the
     * thread ends or {@link #HOLD_TTL} passes); a cold load starts right away so it
     * overlaps the agent's triage.
     */
    public void commit(String threadId, List<String> candidates, Supplier<Optional<Placement>> planner) {
        commit(threadId, System.currentTimeMillis(), candidates, planner);
    }

    /**
     * {@link #commit(String, List, Supplier)} for a selection made at {@code selectedAtMs}:
     * a first call, stand-aside, or thread end recorded at or after that moment
     * settles the thread, so the plan is dropped instead of held.
     */
    public void commit(String threadId, long selectedAtMs, List<String> candidates,
                       Supplier<Optional<Placement>> planner) {
        pruneSettled();
        if (isSettledSince(threadId, selectedAtMs)) {
            return;
        }
        if (demand != null) {
            demand.intend(threadId, candidates);
            demand.recordSelection(candidates);
        }
        Optional<Placement> plan = planner.get();
        if (plan.isEmpty()) {
            return;
        }
        Placement p = plan.get();
        if (isSettledSince(threadId, selectedAtMs)) {
            // The first call started, or the agent stood aside, while this plan was made.
            tickets.release(p.pick().ticket());
            return;
        }
        ScheduledFuture<?> expiry = timers.schedule(() -> release(threadId), HOLD_TTL.toMillis(), TimeUnit.MILLISECONDS);
        Held previous = held.put(threadId, new Held(p, expiry));
        if (previous != null) {
            drop(previous);
        }
        if (p.coldLoad()) {
            selector.driverFor(p.pick().server()).warm(p.pick().server(), p.model());
        }
        log.info("Planned first call for thread {}: {} on {}{}", threadId, p.model(), p.pick().server().name(),
                p.coldLoad() ? " (loading now)" : "");
    }

    /** The plan held for this thread's first call, handed over once; the caller owns its ticket. */
    public Optional<Placement> takeHeld(String threadId) {
        Held h = threadId == null ? null : held.remove(threadId);
        if (h == null) {
            return Optional.empty();
        }
        h.expiry().cancel(false);
        return Optional.of(h.placement());
    }

    /** The agent stood aside or the thread ended: release any held claim and clear its intents. */
    public void release(String threadId) {
        if (threadId == null) {
            return;
        }
        settled.put(threadId, System.currentTimeMillis());
        Held h = held.remove(threadId);
        if (h != null) {
            drop(h);
            log.info("Released the planned claim for thread {}", threadId);
        }
        if (demand != null) {
            demand.clearIntents(threadId);
        }
    }

    private void drop(Held h) {
        h.expiry().cancel(false);
        tickets.release(h.placement().pick().ticket());
    }

    /** A call started on {@code model}: the agent's intents are fulfilled, and the use is recorded. */
    public void callStarted(String threadId, String model) {
        if (threadId != null) {
            settled.put(threadId, System.currentTimeMillis());
        }
        if (demand == null) {
            return;
        }
        if (threadId != null) {
            demand.clearIntents(threadId);
        }
        demand.recordUse(model);
    }

    private boolean isSettledSince(String threadId, long sinceMs) {
        Long at = settled.get(threadId);
        return at != null && at >= sinceMs;
    }

    /**
     * Forgets settled threads older than twice {@link #HOLD_TTL}. A plan task for a
     * selection that old can no longer be pending; the window is doubled so a plan
     * delayed on a busy executor still sees that its thread settled.
     */
    private void pruneSettled() {
        long cutoff = System.currentTimeMillis() - 2 * HOLD_TTL.toMillis();
        settled.values().removeIf(at -> at < cutoff);
    }

    /** This agent started waiting for a GPU for {@code model}. */
    public void waitStarted(String model) {
        if (demand != null) {
            demand.waitStarted(model);
        }
    }

    /** This agent's wait for {@code model} ended. */
    public void waitEnded(String model) {
        if (demand != null) {
            demand.waitEnded(model);
        }
    }

    // Visible for testing
    boolean holds(String threadId) {
        return held.containsKey(threadId);
    }
}
