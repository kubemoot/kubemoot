package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

/** The event envelope: what goes on the bus, and how a reader treats what it did not expect. */
class CoordinatorEventTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private static byte[] bytes(String json) {
        return json.getBytes(StandardCharsets.UTF_8);
    }

    @Test
    void anEventSurvivesTheWire() throws Exception {
        ObjectNode data = MAPPER.createObjectNode().put("agent", "metrics");
        var event = new CoordinatorEvent("t1/0/signal/metrics/m1", "signal_applied", "t1", 2,
                Instant.parse("2026-10-31T12:00:00Z"), "coordinator", data);

        var back = CoordinatorEvent.parse(MAPPER, MAPPER.writeValueAsBytes(event.toJson(MAPPER)));

        assertEquals(event, back);
    }

    @Test
    void theEnvelopeCarriesTheVersionAndEveryField() {
        var event = new CoordinatorEvent("id", "thread_closed", "t1", 0, Instant.parse("2026-10-31T12:00:00Z"),
                "coordinator", MAPPER.createObjectNode());

        var json = event.toJson(MAPPER);

        assertEquals(CoordinatorEvent.VERSION, json.get("v").asInt());
        assertEquals("id", json.get("eventId").asText());
        assertEquals("thread_closed", json.get("type").asText());
        assertEquals("t1", json.get("threadId").asText());
        assertEquals("2026-10-31T12:00:00Z", json.get("at").asText());
        assertEquals("coordinator", json.get("coordinator").asText());
        assertTrue(json.get("data").isObject());
    }

    @Test
    void anEventOfAnUnknownTypeStillParses_soTheReducerCanSkipIt() {
        var event = CoordinatorEvent.parse(MAPPER,
                bytes("{\"v\":7,\"eventId\":\"x\",\"type\":\"from_the_future\",\"threadId\":\"t1\",\"extra\":true}"));

        assertNotNull(event);
        assertEquals("from_the_future", event.type());
    }

    @Test
    void anEventMissingItsIdTypeOrThreadIsNotAnEvent() {
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("{\"type\":\"a\",\"threadId\":\"t\"}")));
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("{\"eventId\":\"a\",\"threadId\":\"t\"}")));
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("{\"eventId\":\"a\",\"type\":\"t\"}")));
    }

    @Test
    void textThatIsNotAnObjectIsNotAnEvent() {
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("not json")));
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("[1,2]")));
        assertNull(CoordinatorEvent.parse(MAPPER, bytes("")));
    }

    @Test
    void aMissingOrBadTimeBecomesTheEpoch_andMissingDataBecomesAnEmptyObject() {
        var event = CoordinatorEvent.parse(MAPPER,
                bytes("{\"eventId\":\"a\",\"type\":\"t\",\"threadId\":\"x\",\"at\":\"yesterday\"}"));

        assertEquals(Instant.EPOCH, event.at());
        assertTrue(event.data().isEmpty());
        assertEquals(0, event.round());
    }
}
