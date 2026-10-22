package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.io.IOException;
import java.nio.charset.StandardCharsets;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

class RequestQueueConsumerRequesterTest {

    private static RequestQueueConsumer.RequestMessage parse(String extraFields) throws IOException {
        String json = "{\"message\":\"hi\",\"conversationId\":\"c\",\"crew\":\"x\"" + extraFields + "}";
        return RequestQueueConsumer.parseRequest(json.getBytes(StandardCharsets.UTF_8));
    }

    @Test
    void aRequestWithZoneAndLocalTimeCarriesTheRequester() throws IOException {
        var request = parse(",\"timeZone\":\"America/New_York\",\"localDateTime\":\"2026-10-22T20:15:00\"");
        assertEquals("America/New_York", request.requester().timeZone());
        assertEquals("2026-10-22", request.requester().requestDate());
    }

    @Test
    void aRequestWithoutThemHasNoRequester() throws IOException {
        assertNull(parse("").requester());
    }

    @Test
    void invalidValuesGiveNoRequester() throws IOException {
        assertNull(parse(",\"timeZone\":\"Nowhere/Land\",\"localDateTime\":\"later\"").requester());
        assertNull(parse(",\"timeZone\":42,\"localDateTime\":false").requester());
    }
}
