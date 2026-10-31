package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.JsonNodeFactory;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.io.IOException;
import java.time.Instant;
import java.time.format.DateTimeParseException;

/**
 * One decision the coordinator made about a thread, as it is written to the thread's
 * coordinator log ({@code kubemoot.coordinator.<ns>.<crew>.<threadId>}). The envelope is
 * the same for every type; {@code data} holds the fields of the type. The reducer
 * ({@link CoordinatorStateReducer}) turns a sequence of events back into the thread's
 * state, so an event carries what a replay needs and nothing it can read elsewhere.
 *
 * <p>{@code eventId} is a natural key: the same decision made twice has the same id, so a
 * duplicate is ignored by the stream (the id is also the {@code Nats-Msg-Id}) and by the
 * reducer. Built and read with {@code readTree} only, never record binding, because
 * record binding fails silently in the native image.
 */
record CoordinatorEvent(String eventId, String type, String threadId, int round, Instant at,
                        String coordinator, ObjectNode data) {

    /** The envelope version this runtime writes. */
    static final int VERSION = 1;

    static final String THREAD_OPENED = "thread_opened";
    static final String PHASE_ENTERED = "phase_entered";
    static final String SUBCOMMITTEE_CONVENED = "subcommittee_convened";
    static final String EVALUATION_STARTED = "evaluation_started";
    static final String REVIEW_STARTED = "review_started";
    static final String SIGNAL_APPLIED = "signal_applied";
    static final String FAILURE_RECORDED = "failure_recorded";
    static final String SETTLED = "settled";
    static final String SYNTHESIS_STARTED = "synthesis_started";
    static final String SYNTHESIS_DONE = "synthesis_done";
    static final String THREAD_REOPENED = "thread_reopened";
    static final String THREAD_CLOSED = "thread_closed";

    /** The event as the JSON object that goes on the bus. */
    ObjectNode toJson(ObjectMapper mapper) {
        ObjectNode node = mapper.createObjectNode();
        node.put("v", VERSION);
        node.put("eventId", eventId);
        node.put("type", type);
        node.put("threadId", threadId);
        node.put("round", round);
        node.put("at", at.toString());
        node.put("coordinator", coordinator);
        node.set("data", data);
        return node;
    }

    /**
     * The event in {@code bytes}, or null when it is not one: not JSON, or missing the id,
     * the type or the thread. A type this runtime does not know is still an event; the
     * reducer skips it.
     */
    static CoordinatorEvent parse(ObjectMapper mapper, byte[] bytes) {
        try {
            return fromJson(mapper.readTree(bytes));
        } catch (IOException e) {
            return null;
        }
    }

    static CoordinatorEvent fromJson(JsonNode node) {
        if (node == null || !node.isObject()) {
            return null;
        }
        String id = node.path("eventId").asText("");
        String type = node.path("type").asText("");
        String threadId = node.path("threadId").asText("");
        if (id.isEmpty() || type.isEmpty() || threadId.isEmpty()) {
            return null;
        }
        JsonNode data = node.path("data");
        return new CoordinatorEvent(id, type, threadId, node.path("round").asInt(0),
                instant(node.path("at").asText("")), node.path("coordinator").asText(""),
                data instanceof ObjectNode object ? object : JsonNodeFactory.instance.objectNode());
    }

    /** The instant in {@code text}, or the epoch when it is blank or malformed. */
    static Instant instant(String text) {
        try {
            return Instant.parse(text);
        } catch (DateTimeParseException e) {
            return Instant.EPOCH;
        }
    }
}
