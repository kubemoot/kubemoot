package ai.kubemoot.agent.modelserver;

import ai.kubemoot.agent.modelserver.PlacementWire.PlaceReply;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceRequest;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.function.Supplier;

/**
 * {@link CallPlacement} that asks the placement service, which owns the tickets and holds the plans
 * made at selection. The caller code is the same as for in-process placement ({@link CallPlanner}).
 *
 * <p><b>When the service cannot answer.</b> {@link #place} and the other calls that decide something
 * throw {@link PlacementUnavailableException}; the caller places the call on the crew's declared
 * server or on {@link #lastKnownServer}, and says so in the call's {@code pickReason}. Calls that only
 * report (a release, a residency, an outcome, demand) never fail the call: a lost release leaves a
 * ticket the service drops after its TTL, which is the same safety net a crashed holder relies on.</p>
 *
 * <p>Reports go out on one background thread so they add nothing to the call; the plan made at
 * selection is taken only for threads this process planned.</p>
 */
public class RemoteCallPlanner implements CallPlacement {

    private static final Logger log = LoggerFactory.getLogger(RemoteCallPlanner.class);
    private static final String API = "/placement/v1";
    private static final int REPORT_QUEUE = 2_000;
    private static final long WARN_INTERVAL_MS = 30_000L;

    private final PlacementClient client;
    private final ServerSelector selector;
    private final AgentIdentity identity;
    private final String policy;
    private final ObjectMapper mapper;
    private final MeterRegistry registry;
    /** Threads this process planned, so only they pay for a take. */
    private final Set<String> planned = ConcurrentHashMap.newKeySet();
    private final Map<String, String> lastServerByModel = new ConcurrentHashMap<>();
    private final Map<String, Integer> activeByServer = new ConcurrentHashMap<>();
    private final ExecutorService reports = new ThreadPoolExecutor(1, 1, 0L, TimeUnit.MILLISECONDS,
            new ArrayBlockingQueue<>(REPORT_QUEUE), r -> {
                Thread t = new Thread(r, "placement-reports");
                t.setDaemon(true);
                return t;
            }, new ThreadPoolExecutor.DiscardPolicy());
    private volatile long lastWarnMs;

    public RemoteCallPlanner(PlacementClient client, ServerSelector selector, AgentIdentity identity, String policy,
                             ObjectMapper mapper, MeterRegistry registry) {
        this.client = client;
        this.selector = selector;
        this.identity = identity;
        this.policy = policy == null ? "" : policy;
        this.mapper = mapper;
        this.registry = registry;
    }

    @Override
    public PlacementMode mode() {
        return PlacementMode.parse(policy).orElse(PlacementMode.ALGORITHM);
    }

    // ---- placing a call ----

    @Override
    public Optional<CallPlanner.Placement> place(CallPlanner.PlacementRequest request) {
        PlaceRequest wire = PlaceRequest.of(identity, "", "", policy, request);
        PlacementClient.Answer answer = guarded("place",
                () -> client.post(API + "/place", PlacementWire.requestJson(mapper, wire)));
        if (answer.status() == 409 && answer.body() != null) {
            // Busy, or a refusal waiting cannot cure: the caller's own checks (the same code the
            // service ran) tell the two apart, exactly as for an in-process placement that found no room.
            log.debug("Placement service found no room: {}", answer.body());
            return Optional.empty();
        }
        return Optional.of(placement(answer, "place"));
    }

    private CallPlanner.Placement placement(PlacementClient.Answer answer, String op) {
        if (answer.status() != 200 || answer.body() == null) {
            throw unavailable(op, "answered " + answer.status());
        }
        PlaceReply reply = PlacementWire.replyFrom(answer.body());
        ServerState server = serverNamed(reply.server());
        if (server == null) {
            releaseTicket(reply.ticket());
            throw unavailable(op, "placed the call on " + reply.server() + ", which this agent does not see");
        }
        lastServerByModel.put(reply.model(), reply.server());
        activeByServer.put(reply.server(), reply.active());
        return reply.toPlacement(server);
    }

    private ServerState serverNamed(String name) {
        for (int attempt = 0; attempt < 2; attempt++) {
            for (ServerState s : selector.readState()) {
                if (s.name().equals(name)) {
                    return s;
                }
            }
            selector.invalidateCache();
        }
        return null;
    }

    // ---- the plan made at selection ----

    @Override
    public void commitPlan(String threadId, long selectedAtMs, List<String> candidates,
                           Supplier<Optional<CallPlanner.PlacementRequest>> request) {
        Optional<CallPlanner.PlacementRequest> r = request.get();
        if (r.isEmpty()) {
            return;
        }
        ObjectNode body = PlacementWire.requestJson(mapper,
                PlaceRequest.of(identity, threadId, "mulling", policy, r.get()));
        body.put("selectedAtMs", selectedAtMs);
        var cands = body.putArray("candidates");
        candidates.forEach(cands::add);
        try {
            planned.add(threadId);
            client.post(API + "/threads/" + threadId + "/plan", body);
        } catch (PlacementUnavailableException e) {
            // A plan only saves time at the first call; without it the call is placed when it is made.
            planned.remove(threadId);
            warnUnavailable("plan", e);
        }
    }

    @Override
    public Optional<CallPlanner.Placement> takeHeld(String threadId) {
        if (threadId == null || !planned.remove(threadId)) {
            return Optional.empty();
        }
        try {
            PlacementClient.Answer answer = client.post(API + "/threads/" + threadId + "/take",
                    PlacementWire.identityJson(mapper, identity));
            return answer.status() == 200 ? Optional.of(placement(answer, "take")) : Optional.empty();
        } catch (PlacementUnavailableException e) {
            warnUnavailable("take", e);
            return Optional.empty();
        }
    }

    @Override
    public void release(String threadId) {
        if (threadId == null) {
            return;
        }
        planned.remove(threadId);
        report("release-thread", () -> client.delete(API + "/threads/" + threadId + "?namespace="
                + enc(identity.namespace()) + "&crew=" + enc(identity.crew()) + "&agent=" + enc(identity.agentName())));
    }

    // ---- demand ----

    @Override
    public void callStarted(String threadId, String model) {
        ObjectNode body = PlacementWire.identityJson(mapper, identity);
        body.put("model", model == null ? "" : model);
        String thread = threadId == null || threadId.isBlank() ? "-" : threadId;
        report("call-started", () -> client.post(API + "/threads/" + thread + "/call-started", body));
    }

    @Override
    public void waitStarted(String model) {
        waiting(model, true);
    }

    @Override
    public void waitEnded(String model) {
        waiting(model, false);
    }

    private void waiting(String model, boolean active) {
        ObjectNode body = PlacementWire.identityJson(mapper, identity);
        body.put("model", model);
        body.put("active", active);
        report("waits", () -> client.post(API + "/waits", body));
    }

    // ---- after a call ----

    @Override
    public void releaseTicket(Ticket ticket) {
        if (ticket == null) {
            return;
        }
        try {
            client.post(API + "/release", mapper.createObjectNode().put("ticket", ticket.ticketId()));
        } catch (PlacementUnavailableException e) {
            // The service drops a ticket nobody released after its TTL.
            log.debug("Releasing ticket {} failed: {}", ticket.ticketId(), e.getMessage());
        }
    }

    @Override
    public void recordResidency(String server, String model, long footprintMiB) {
        ObjectNode body = mapper.createObjectNode().put("server", server).put("model", model)
                .put("footprintMiB", footprintMiB);
        report("residency", () -> client.post(API + "/residency", body));
    }

    @Override
    public int activeCountFor(String server) {
        return activeByServer.getOrDefault(server, 0);
    }

    @Override
    public void recordOutcome(String server, String model, long durationMs, boolean ok) {
        if (server == null || server.isEmpty()) {
            return;
        }
        ObjectNode body = mapper.createObjectNode().put("server", server).put("durationMs", durationMs)
                .put("ok", ok);
        if (model != null) {
            body.put("model", model);
        }
        report("outcomes", () -> client.post(API + "/outcomes", body));
    }

    @Override
    public void recordFailure(String server) {
        report("failures", () -> client.post(API + "/failures", mapper.createObjectNode().put("server", server)));
    }

    @Override
    public Optional<String> lastKnownServer(String model) {
        return Optional.ofNullable(lastServerByModel.get(model));
    }

    // ---- plumbing ----

    private PlacementClient.Answer guarded(String op, Supplier<PlacementClient.Answer> call) {
        try {
            return call.get();
        } catch (PlacementUnavailableException e) {
            warnUnavailable(op, e);
            throw e;
        }
    }

    private PlacementUnavailableException unavailable(String op, String why) {
        var e = new PlacementUnavailableException("placement service " + why);
        warnUnavailable(op, e);
        return e;
    }

    /** Counts every miss and logs at most one in {@link #WARN_INTERVAL_MS}, so an outage is visible and not a flood. */
    private void warnUnavailable(String op, PlacementUnavailableException e) {
        Counter.builder("kubemoot_placement_client_unavailable_total")
                .description("Calls to the placement service that got no answer, by operation")
                .tag("op", op).register(registry).increment();
        long now = System.currentTimeMillis();
        if (now - lastWarnMs >= WARN_INTERVAL_MS) {
            lastWarnMs = now;
            log.warn("Placement service did not answer {} ({}); calls fall back to the declared or last-known server",
                    op, e.getMessage());
        } else {
            log.debug("Placement service did not answer {}: {}", op, e.getMessage());
        }
    }

    /** Sends a report off the call's path; a failure is counted and dropped. */
    private void report(String op, Supplier<PlacementClient.Answer> call) {
        reports.execute(() -> {
            try {
                call.get();
            } catch (PlacementUnavailableException e) {
                warnUnavailable(op, e);
            }
        });
    }

    private static String enc(String s) {
        return java.net.URLEncoder.encode(s == null ? "" : s, java.nio.charset.StandardCharsets.UTF_8);
    }
}
