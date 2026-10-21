package ai.kubemoot.placement;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import jakarta.inject.Inject;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import jakarta.ws.rs.ext.ExceptionMapper;
import jakarta.ws.rs.ext.Provider;

/** Maps what the engine throws to the API's error replies: {@code {"code": ..., "reason": ...}}. */
public final class ErrorMappers {

    private ErrorMappers() {}

    static Response reply(ObjectMapper mapper, Response.Status status, String code, String reason) {
        ObjectNode body = mapper.createObjectNode();
        body.put("code", code);
        body.put("reason", reason == null ? "" : reason);
        return Response.status(status).type(MediaType.APPLICATION_JSON).entity(body).build();
    }

    /** A request that does not say what to place. */
    public static final class InvalidRequestException extends RuntimeException {
        public InvalidRequestException(String message) {
            super(message);
        }
    }

    @Provider
    public static class BadRequest implements ExceptionMapper<InvalidRequestException> {
        private final ObjectMapper mapper;

        @Inject
        public BadRequest(ObjectMapper mapper) {
            this.mapper = mapper;
        }

        @Override
        public Response toResponse(InvalidRequestException e) {
            return reply(mapper, Response.Status.BAD_REQUEST, "bad_request", e.getMessage());
        }
    }

    /** A request that reached a replica that is not serving. */
    @Provider
    public static class NotLeader implements ExceptionMapper<PlacementEngine.NotLeaderException> {
        private final ObjectMapper mapper;

        @Inject
        public NotLeader(ObjectMapper mapper) {
            this.mapper = mapper;
        }

        @Override
        public Response toResponse(PlacementEngine.NotLeaderException e) {
            return reply(mapper, Response.Status.SERVICE_UNAVAILABLE, "not_leader", e.getMessage());
        }
    }

    /** No server state is published, so nothing can be placed; callers fall back as they do for an outage. */
    @Provider
    public static class NoServerState implements ExceptionMapper<PlacementEngine.NoServerStateException> {
        private final ObjectMapper mapper;

        @Inject
        public NoServerState(ObjectMapper mapper) {
            this.mapper = mapper;
        }

        @Override
        public Response toResponse(PlacementEngine.NoServerStateException e) {
            return reply(mapper, Response.Status.SERVICE_UNAVAILABLE, "no_server_state", e.getMessage());
        }
    }
}
