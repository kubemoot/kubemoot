package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.AgentIdentity;
import ai.kubemoot.agent.modelserver.AlgorithmPlacement;
import ai.kubemoot.agent.modelserver.CallPlanner;
import ai.kubemoot.agent.modelserver.DemandBoard;
import ai.kubemoot.agent.modelserver.NoFitException;
import ai.kubemoot.agent.modelserver.PlacementFeasibility;
import ai.kubemoot.agent.modelserver.PlacementMode;
import ai.kubemoot.agent.modelserver.PlacementPolicies;
import ai.kubemoot.agent.modelserver.PlacementPolicy;
import ai.kubemoot.agent.modelserver.PlacementWire;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceReply;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceRequest;
import ai.kubemoot.agent.modelserver.PlacementWire.Refusal;
import ai.kubemoot.agent.modelserver.ServerSelector;
import ai.kubemoot.agent.modelserver.ServerState;
import ai.kubemoot.agent.modelserver.TicketManager;
import ai.kubemoot.agent.nats.NatsLink;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.MeterRegistry;
import jakarta.inject.Inject;
import jakarta.inject.Singleton;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

/**
 * Places the calls of every agent, with the placement code agent-runtime runs in-process. What is
 * shared across agents is shared here: the tickets (one table, one writer), the server state and
 * the circuit breakers. What belongs to one agent is kept per agent identity: its demand on the
 * shared view, the plans made when the coordinator selected it, and its placement policy.
 *
 * <p>The service answers a call that finds no room with the reason: busy (wait for a capacity
 * change), or a refusal no wait can cure ({@link PlacementFeasibility}).</p>
 */
@Singleton
public class PlacementEngine {

    private static final Logger log = LoggerFactory.getLogger(PlacementEngine.class);
    private static final int BACKGROUND_QUEUE = 1_000;

    /** The answer to a placement: a reply, or the reason there is none. */
    public record Result(PlaceReply reply, Refusal refusal) {
        static Result placed(PlaceReply reply) {
            return new Result(reply, null);
        }

        static Result refused(Refusal refusal) {
            return new Result(null, refusal);
        }
    }

    /** No server state is published, so nothing can be placed; the caller falls back. */
    public static final class NoServerStateException extends RuntimeException {
        NoServerStateException(String why) {
            super(why);
        }
    }

    /** This replica is not serving (not the leader, or the lease fence passed); the caller tries again or falls back. */
    public static final class NotLeaderException extends RuntimeException {
        NotLeaderException() {
            super("this replica is not the placement leader");
        }
    }

    /** The placement state of one agent, and when it was last used. */
    private static final class AgentPlacement {
        private final String policy;
        private final CallPlanner planner;
        private volatile long lastUsedNanos = System.nanoTime();

        AgentPlacement(String policy, CallPlanner planner) {
            this.policy = policy;
            this.planner = planner;
        }

        String policy() {
            return policy;
        }

        CallPlanner planner() {
            lastUsedNanos = System.nanoTime();
            return planner;
        }

        boolean idleFor(Duration d) {
            return System.nanoTime() - lastUsedNanos > d.toNanos() && planner.heldCount() == 0;
        }

        void close() {
            planner.close();
        }
    }

    /** How long an agent that holds no plan can go unused before its state is dropped. */
    private static final Duration IDLE_AGENT = Duration.ofMinutes(30);
    private static final long EVICTION_INTERVAL_NANOS = TimeUnit.MINUTES.toNanos(1);

    private final ServerSelector selector;
    private final TicketManager tickets;
    private final TicketTable table;
    private final NatsLink nats;
    private final ObjectMapper mapper;
    private final ServiceSettings settings;
    private final MeterRegistry registry;
    private final Map<AgentIdentity, AgentPlacement> agents = new ConcurrentHashMap<>();
    private final AtomicLong nextEvictionNanos = new AtomicLong(System.nanoTime() + EVICTION_INTERVAL_NANOS);
    /** Bookkeeping that is not on a call's critical path (demand writes); dropped when it backs up. */
    private final ExecutorService background = new ThreadPoolExecutor(1, 1, 0L, TimeUnit.MILLISECONDS,
            new ArrayBlockingQueue<>(BACKGROUND_QUEUE), r -> {
                Thread t = new Thread(r, "placement-background");
                t.setDaemon(true);
                return t;
            }, new ThreadPoolExecutor.DiscardPolicy());

    @Inject
    public PlacementEngine(ServerSelector selector, TicketManager tickets, TicketTable table, NatsLink nats,
                           ObjectMapper mapper, ServiceSettings settings, MeterRegistry registry) {
        this.selector = selector;
        this.tickets = tickets;
        this.table = table;
        this.nats = nats;
        this.mapper = mapper;
        this.settings = settings;
        this.registry = registry;
    }

    // ---- placing a call ----

    /**
     * Places one call. The claimed ticket is the caller's until it gives it back with
     * {@link #release}.
     *
     * @throws NoServerStateException when no server state is published
     */
    public Result place(PlaceRequest r) {
        requireServing();
        List<ServerState> states = readStateOrFail();
        var request = r.toPlacementRequest(states);
        try {
            Optional<CallPlanner.Placement> placed = agent(r.identity(), r.policy()).planner().place(request);
            if (placed.isPresent()) {
                String server = placed.get().pick().server().name();
                return Result.placed(PlaceReply.of(placed.get(), tickets.activeCountFor(server)));
            }
        } catch (NoFitException e) {
            return Result.refused(new Refusal(e.reason(), e.predictorReason()));
        }
        // A claim refused because the lease fence passed is not "busy": the caller must not wait here.
        requireServing();
        return Result.refused(whyNotPlaced(states, r, request));
    }

    private void requireServing() {
        if (!table.available()) {
            throw new NotLeaderException();
        }
    }

    /** Why a call that found no room did not: busy, or a refusal a wait cannot cure. */
    private Refusal whyNotPlaced(List<ServerState> states, PlaceRequest r, CallPlanner.PlacementRequest request) {
        String preferred = request.preferred();
        return PlacementFeasibility.refusal(states, preferred, request.footprint().applyAsLong(preferred),
                        request.acceptable(), r.promptTokens(), request.footprint(), selector::activeModelsOn)
                .map(f -> new Refusal(f.code(), f.detail()))
                .orElseGet(() -> new Refusal(PlacementWire.NO_FIT,
                        "every accelerator that can hold " + preferred + " is busy"));
    }

    private List<ServerState> readStateOrFail() {
        List<ServerState> states = selector.readState();
        if (states.isEmpty()) {
            throw new NoServerStateException(selector.lastReadError().orElse("no server state published"));
        }
        return states;
    }

    /** Gives a claimed ticket back. Idempotent: an unknown or already released id changes nothing. */
    public boolean release(String ticketId) {
        return table.deleteTicket(ticketId);
    }

    // ---- thread-scoped state ----

    /**
     * The agent was selected for {@code threadId}: record its intent for {@code candidates} and plan its
     * first call now, claiming capacity and starting a needed load so it overlaps the agent's triage.
     */
    public void plan(PlaceRequest r, long selectedAtMs, List<String> candidates) {
        requireServing();
        agent(r.identity(), r.policy()).planner().commitPlan(r.threadId(), selectedAtMs, candidates, () -> {
            List<ServerState> states = selector.readState();
            return states.isEmpty() ? Optional.empty() : Optional.of(r.toPlacementRequest(states));
        });
    }

    /** The plan held for the thread's first call, handed over once; the caller owns its ticket. */
    public Optional<PlaceReply> take(AgentIdentity identity, String threadId) {
        requireServing();
        return agent(identity, "").planner().takeHeld(threadId)
                .map(p -> PlaceReply.of(p, tickets.activeCountFor(p.pick().server().name())));
    }

    /** The agent stood aside or the thread ended: release its plan and clear its intents. */
    public void releaseThread(AgentIdentity identity, String threadId) {
        agent(identity, "").planner().release(threadId);
    }

    /** A call started on {@code model}; recorded off the call's path. */
    public void callStarted(AgentIdentity identity, String threadId, String model) {
        background.execute(() -> agent(identity, "").planner().callStarted(threadId, model));
    }

    /** The agent started or stopped waiting for capacity for {@code model}; recorded off the call's path. */
    public void waiting(AgentIdentity identity, String model, boolean active) {
        background.execute(() -> {
            var planner = agent(identity, "").planner();
            if (active) {
                planner.waitStarted(model);
            } else {
                planner.waitEnded(model);
            }
        });
    }

    // ---- what happened after a call ----

    /** A finished call: its time on {@code server} and whether it succeeded. */
    public void outcome(String server, String model, long durationMs, boolean ok) {
        selector.recordCallOutcome(server, model, durationMs, ok);
    }

    /** The server failed a caller before its call could start. */
    public void failure(String server) {
        selector.recordFailure(server);
    }

    /** {@code model} is resident on {@code server}; other callers see it at once. */
    public void residency(String server, String model, long footprintMiB) {
        tickets.recordResidency(server, model, footprintMiB);
    }

    // ---- per-agent state ----

    /**
     * The state of {@code identity}. A caller that names a policy different from the one in use
     * replaces the state; a caller that names none keeps what is there.
     */
    private AgentPlacement agent(AgentIdentity identity, String policy) {
        String wanted = policy == null || policy.isBlank() ? null : PlacementMode.parse(policy)
                .map(PlacementMode::configName).orElse(PlacementMode.ALGORITHM.configName());
        AgentPlacement found = agents.compute(identity, (id, current) -> {
            if (current != null && (wanted == null || wanted.equals(current.policy()))) {
                return current;
            }
            if (current != null) {
                log.info("Placement policy for {} changed from {} to {}", id.agentId(), current.policy(), wanted);
                current.close();
            }
            return build(id, wanted == null ? settings.defaultPolicy() : wanted);
        });
        forgetIdleAgents();
        return found;
    }

    /** At most once a minute, drops the state of agents that hold no plan and have not called for a while. */
    private void forgetIdleAgents() {
        long due = nextEvictionNanos.get();
        long now = System.nanoTime();
        if (now < due || !nextEvictionNanos.compareAndSet(due, now + EVICTION_INTERVAL_NANOS)) {
            return;
        }
        agents.entrySet().removeIf(e -> {
            boolean idle = e.getValue().idleFor(IDLE_AGENT);
            if (idle) {
                e.getValue().close();
            }
            return idle;
        });
    }

    private AgentPlacement build(AgentIdentity identity, String policyName) {
        DemandBoard demand = new DemandBoard(nats, mapper, identity);
        PlacementPolicy policy = PlacementPolicies.create(policyName,
                new AlgorithmPlacement(selector, tickets, demand), selector,
                PlacementPolicies.shadowRecorder(registry));
        String canonical = PlacementMode.parse(policyName).orElse(PlacementMode.ALGORITHM).configName();
        return new AgentPlacement(canonical, new CallPlanner(selector, tickets, demand, policy));
    }

    /** The agents the engine holds state for. */
    int agentCount() {
        return agents.size();
    }

    /** Releases every plan and forgets every agent: this replica is not the leader any more. */
    void reset() {
        agents.values().forEach(AgentPlacement::close);
        agents.clear();
    }
}
