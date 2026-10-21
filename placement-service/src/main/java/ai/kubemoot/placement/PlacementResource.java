package ai.kubemoot.placement;

import ai.kubemoot.agent.modelserver.AgentIdentity;
import ai.kubemoot.agent.modelserver.PlacementWire;
import ai.kubemoot.agent.modelserver.PlacementWire.PlaceRequest;
import ai.kubemoot.placement.PlacementEngine.Result;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import jakarta.inject.Inject;
import jakarta.ws.rs.Consumes;
import jakarta.ws.rs.DELETE;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.POST;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.QueryParam;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Supplier;

/**
 * The placement API, version 1. Every call is JSON; every reply is JSON.
 *
 * <ul>
 *   <li>{@code POST /place}: places one call. 200 with where it runs and the claimed ticket, or 409
 *       with a refusal code: {@code kubemoot_no_fit} (busy, wait for a capacity change),
 *       {@code model-too-large}, {@code no-server-has-model}, {@code prompt-too-large} (no wait
 *       will place it).</li>
 *   <li>{@code POST /release}: gives a ticket back.</li>
 *   <li>{@code POST /threads/{id}/plan}, {@code POST /threads/{id}/take}, {@code DELETE /threads/{id}}:
 *       the plan made when the coordinator selects an agent for a thread, handed to its first call, or
 *       released when the thread ends.</li>
 *   <li>{@code POST /threads/{id}/call-started}, {@code POST /waits}: the demand a caller adds to the
 *       shared view.</li>
 *   <li>{@code GET /capacity}, {@code POST /capacity/await}: the change counter a caller waits on when
 *       its call found no room.</li>
 *   <li>{@code POST /outcomes}, {@code POST /failures}, {@code POST /residency}: what happened after a call.</li>
 *   <li>{@code GET /tickets}: the tickets held now.</li>
 * </ul>
 *
 * <p>Only the leader answers; a standby answers 503 {@code not_leader} (see {@link LeaderFilter}).</p>
 */
@Path("/placement/v1")
@Consumes(MediaType.APPLICATION_JSON)
@Produces(MediaType.APPLICATION_JSON)
public class PlacementResource {

    private static final Duration MAX_AWAIT = Duration.ofSeconds(30);

    private final PlacementEngine engine;
    private final CapacityVersion capacity;
    private final TicketTable table;
    private final ObjectMapper mapper;
    private final MeterRegistry registry;
    private final Map<String, Timer> timers = new ConcurrentHashMap<>();
    private final Map<String, Counter> counters = new ConcurrentHashMap<>();

    @Inject
    public PlacementResource(PlacementEngine engine, CapacityVersion capacity, TicketTable table,
                             ObjectMapper mapper, MeterRegistry registry) {
        this.engine = engine;
        this.capacity = capacity;
        this.table = table;
        this.mapper = mapper;
        this.registry = registry;
    }

    @POST
    @Path("/place")
    public Response place(JsonNode body) {
        return timed("place", () -> {
            Result result = engine.place(parse(body));
            count("place", result.reply() != null ? "placed" : result.refusal().code());
            if (result.reply() != null) {
                return Response.ok(PlacementWire.replyJson(mapper, result.reply())).build();
            }
            return Response.status(Response.Status.CONFLICT)
                    .entity(PlacementWire.refusalJson(mapper, result.refusal())).build();
        });
    }

    @POST
    @Path("/release")
    public Response release(JsonNode body) {
        return timed("release", () -> {
            boolean held = engine.release(body.path("ticket").asText(""));
            count("release", held ? "released" : "unknown");
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/threads/{threadId}/plan")
    public Response plan(@PathParam("threadId") String threadId, JsonNode body) {
        return timed("plan", () -> {
            PlaceRequest base = parse(body);
            PlaceRequest request = new PlaceRequest(base.identity(), threadId, base.phase(), base.policy(),
                    base.models(), base.promptTokens(), base.mayUnload());
            List<String> candidates = new ArrayList<>();
            body.path("candidates").forEach(c -> candidates.add(c.asText()));
            engine.plan(request, body.path("selectedAtMs").asLong(System.currentTimeMillis()), candidates);
            return Response.accepted().build();
        });
    }

    @POST
    @Path("/threads/{threadId}/take")
    public Response take(@PathParam("threadId") String threadId, JsonNode body) {
        return timed("take", () -> engine.take(PlacementWire.identityFrom(body), threadId)
                .map(r -> Response.ok(PlacementWire.replyJson(mapper, r)).build())
                .orElseGet(() -> Response.noContent().build()));
    }

    @DELETE
    @Path("/threads/{threadId}")
    public Response releaseThread(@PathParam("threadId") String threadId, @QueryParam("namespace") String namespace,
                                  @QueryParam("crew") String crew, @QueryParam("agent") String agent) {
        return timed("release-thread", () -> {
            engine.releaseThread(new AgentIdentity(namespace, agent, crew), threadId);
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/threads/{threadId}/call-started")
    public Response callStarted(@PathParam("threadId") String threadId, JsonNode body) {
        return timed("call-started", () -> {
            engine.callStarted(PlacementWire.identityFrom(body), threadId, body.path("model").asText(""));
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/waits")
    public Response waits(JsonNode body) {
        return timed("waits", () -> {
            engine.waiting(PlacementWire.identityFrom(body), body.path("model").asText(""),
                    body.path("active").asBoolean(false));
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/outcomes")
    public Response outcomes(JsonNode body) {
        return timed("outcomes", () -> {
            String model = body.hasNonNull("model") ? body.path("model").asText() : null;
            engine.outcome(body.path("server").asText(""), model, body.path("durationMs").asLong(0L),
                    body.path("ok").asBoolean(true));
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/failures")
    public Response failures(JsonNode body) {
        return timed("failures", () -> {
            engine.failure(body.path("server").asText(""));
            return Response.noContent().build();
        });
    }

    @POST
    @Path("/residency")
    public Response residency(JsonNode body) {
        return timed("residency", () -> {
            engine.residency(body.path("server").asText(""), body.path("model").asText(""),
                    body.path("footprintMiB").asLong(0L));
            return Response.noContent().build();
        });
    }

    @GET
    @Path("/capacity")
    public Response capacity() {
        ObjectNode n = mapper.createObjectNode();
        n.put("version", capacity.current());
        return Response.ok(n).build();
    }

    /** Stays open until the capacity version differs from {@code seen}, or {@code maxWaitMs} passes. */
    @POST
    @Path("/capacity/await")
    public CompletionStage<Response> awaitCapacity(JsonNode body) {
        long seen = body.path("seen").asLong(-1L);
        Duration max = Duration.ofMillis(Math.max(0L, Math.min(body.path("maxWaitMs").asLong(25_000L),
                MAX_AWAIT.toMillis())));
        return capacity.awaitChange(seen, max).thenApply(version -> {
            ObjectNode n = mapper.createObjectNode();
            n.put("version", version);
            n.put("changed", version != seen);
            return Response.ok(n).build();
        });
    }

    @GET
    @Path("/tickets")
    public Response tickets() {
        ArrayNode out = mapper.createArrayNode();
        for (var e : table.snapshot()) {
            ObjectNode n = out.addObject();
            n.put("key", e.key());
            n.put("revision", e.revision());
            n.set("doc", e.doc());
        }
        return Response.ok(out).build();
    }

    // ---- instrumentation ----

    private Response timed(String op, Supplier<Response> action) {
        Timer timer = timers.computeIfAbsent(op, o -> Timer.builder("kubemoot_placement_duration_seconds")
                .description("How long the placement service took to answer, by operation")
                .publishPercentileHistogram().tag("op", o).register(registry));
        return timer.record(action);
    }

    private void count(String op, String result) {
        counters.computeIfAbsent(op + "|" + result, k -> Counter.builder("kubemoot_placement_requests_total")
                .description("Placement requests by operation and result")
                .tag("op", op).tag("result", result).register(registry)).increment();
    }

    /** The request in {@code body}; a request that names no model is answered 400. */
    private static PlaceRequest parse(JsonNode body) {
        try {
            return PlacementWire.requestFrom(body);
        } catch (IllegalArgumentException e) {
            throw new ErrorMappers.InvalidRequestException(e.getMessage());
        }
    }
}
