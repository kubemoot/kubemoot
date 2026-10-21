package ai.kubemoot.agent.modelserver;

import io.quarkus.runtime.annotations.RegisterForReflection;
import jakarta.inject.Inject;
import jakarta.ws.rs.Consumes;
import jakarta.ws.rs.POST;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.Produces;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.ConcurrentHashMap;

/**
 * SPIKE (agent sidecar): the placement service API served by the existing placement code.
 * One call places one model call and returns a ticket; a second call releases it.
 */
@Path("/placement/v1")
@Consumes(MediaType.APPLICATION_JSON)
@Produces(MediaType.APPLICATION_JSON)
public class PlacementResource {

    @RegisterForReflection
    public record PlaceRequest(String namespace, String crew, String agent, String threadId, String phase,
                               List<String> models, long promptTokens, long maxOutputTokens,
                               boolean mayUnload) { }

    @RegisterForReflection
    public record PlaceReply(String server, String endpoint, String model, String reason, String ticket,
                             boolean queued, boolean coldLoad, List<String> evicted) { }

    @RegisterForReflection
    public record ReleaseRequest(String ticket) { }

    private final CallPlanner planner;
    private final ServerSelector selector;
    private final TicketManager tickets;
    private final Map<String, Ticket> open = new ConcurrentHashMap<>();

    @Inject
    public PlacementResource(CallPlanner planner, ServerSelector selector, TicketManager tickets) {
        this.planner = planner;
        this.selector = selector;
        this.tickets = tickets;
    }

    @POST
    @Path("/place")
    public Response place(PlaceRequest r) {
        List<ServerState> states = selector.readState();
        if (r.models() == null || r.models().isEmpty()) {
            return Response.status(Response.Status.BAD_REQUEST).entity(Map.of("reason", "models is empty")).build();
        }
        String preferred = r.models().get(0);
        List<String> alts = r.models().subList(1, r.models().size());
        long out = r.maxOutputTokens() <= 0 ? 1024 : r.maxOutputTokens();
        var req = new CallPlanner.PlacementRequest(preferred, alts, states,
                m -> EngineFits.placementFootprintMiB(states, m,
                        states.stream().mapToLong(s -> s.coldLoadFootprintMiB(m)).max().orElse(0L)),
                m -> KvCacheEstimator.estimateMiB(m, r.promptTokens(), out),
                r.promptTokens(), r.mayUnload());
        Optional<CallPlanner.Placement> placed = planner.place(req);
        if (placed.isEmpty()) {
            return Response.status(Response.Status.CONFLICT)
                    .entity(Map.of("code", "kubemoot_no_fit", "reason", "no server has room now")).build();
        }
        CallPlanner.Placement p = placed.get();
        Ticket t = p.pick().ticket();
        if (t != null) {
            open.put(t.ticketId(), t);
        }
        ServerState s = p.pick().server();
        String reason = (p.coldLoad() ? "cold load" : "warm") + (p.queued() ? ", queued" : "")
                + ", policy=" + planner.mode().configName();
        return Response.ok(new PlaceReply(s.name(), s.endpointFor(p.model()), p.model(), reason,
                t == null ? null : t.ticketId(), p.queued(), p.coldLoad(), p.evicted())).build();
    }

    @POST
    @Path("/release")
    public Response release(ReleaseRequest r) {
        Ticket t = open.remove(r.ticket());
        if (t != null) {
            tickets.release(t);
        }
        return Response.noContent().build();
    }
}
