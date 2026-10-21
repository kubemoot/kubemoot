package ai.kubemoot.placement;

import com.fasterxml.jackson.databind.ObjectMapper;
import jakarta.inject.Inject;
import jakarta.ws.rs.container.ContainerRequestContext;
import jakarta.ws.rs.container.ContainerRequestFilter;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import jakarta.ws.rs.ext.Provider;

/**
 * Only the leader answers placement requests. A standby, or a leader that has not renewed its lease
 * within the fence, answers 503 {@code not_leader}; the service only routes to the Ready leader, so
 * this answers the request that arrives in the moments around a handover. A caller treats 503 as
 * "service unavailable" and falls back.
 */
@Provider
public class LeaderFilter implements ContainerRequestFilter {

    private static final String PREFIX = "placement/v1";

    private final Leadership leadership;
    private final ObjectMapper mapper;

    @Inject
    public LeaderFilter(Leadership leadership, ObjectMapper mapper) {
        this.leadership = leadership;
        this.mapper = mapper;
    }

    @Override
    public void filter(ContainerRequestContext request) {
        String path = request.getUriInfo().getPath();
        boolean placement = path.startsWith(PREFIX) || path.startsWith("/" + PREFIX);
        if (placement && !leadership.isLeader()) {
            var body = mapper.createObjectNode();
            body.put("code", "not_leader");
            body.put("reason", "this replica is not the placement leader");
            request.abortWith(Response.status(Response.Status.SERVICE_UNAVAILABLE)
                    .type(MediaType.APPLICATION_JSON).entity(body).build());
        }
    }
}
