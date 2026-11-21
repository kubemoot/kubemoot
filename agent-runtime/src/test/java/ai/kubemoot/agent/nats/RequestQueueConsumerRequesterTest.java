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

    private static RequestQueueConsumer consumer(String defaultZone) {
        var properties = org.mockito.Mockito.mock(ai.kubemoot.agent.config.AgentProperties.class,
                org.mockito.Mockito.RETURNS_DEEP_STUBS);
        org.mockito.Mockito.when(properties.crew()).thenReturn(java.util.Optional.of("c"));
        org.mockito.Mockito.when(properties.defaultTimeZone()).thenReturn(java.util.Optional.ofNullable(defaultZone));
        return new RequestQueueConsumer(null, null, properties);
    }

    // The date is fixed from the moment the gateway queued the question, in the asker's zone.
    @Test
    void theDateIsFixedFromTheTimeTheQuestionWasQueued() throws IOException {
        var consumer = consumer(null);
        var late = parse(",\"timestamp\":\"2026-11-21T04:59:59Z\",\"timeZone\":\"America/New_York\"");
        var early = parse(",\"timestamp\":\"2026-11-21T05:00:01Z\",\"timeZone\":\"America/New_York\"");
        assertEquals("2026-11-20", consumer.arrivedRequester(late).requestDate());
        assertEquals("2026-11-21", consumer.arrivedRequester(early).requestDate());
    }

    @Test
    void withoutAZoneTheCrewDefaultThenUtcApply() throws IOException {
        var queued = parse(",\"timestamp\":\"2026-11-21T04:59:59Z\"");
        var withDefault = consumer("America/New_York").arrivedRequester(queued);
        assertEquals("America/New_York", withDefault.timeZone());
        assertEquals("2026-11-20", withDefault.requestDate());
        var utc = consumer(null).arrivedRequester(queued);
        assertEquals("UTC", utc.timeZone());
        assertEquals("2026-11-21", utc.requestDate());
    }

    @Test
    void anUnreadableOrNullTimestampFallsBackToTheClock() {
        var consumer = consumer(null);
        var noTime = new RequestQueueConsumer.RequestMessage("hi", "c", "x", null, true, true, null);
        var badTime = new RequestQueueConsumer.RequestMessage("hi", "c", "x", "yesterday", true, true, null);
        String today = java.time.LocalDate.now(java.time.ZoneOffset.UTC).toString();
        assertEquals(today, consumer.arrivedRequester(noTime).requestDate());
        assertEquals(today, consumer.arrivedRequester(badTime).requestDate());
    }

    @Test
    void aMissingTimestampFallsBackToTheClock() throws IOException {
        var requester = consumer(null).arrivedRequester(parse(""));
        assertEquals("UTC", requester.timeZone());
        assertEquals(java.time.LocalDate.now(java.time.ZoneOffset.UTC).toString(), requester.requestDate());
    }

    @Test
    void aRequestCarriesThePrincipalTheGatewayVerified() throws IOException {
        var request = parse(",\"principal\":{\"kind\":\"subscriber\",\"issuer\":\"oreilly\",\"id\":\"abc123\"}");
        assertEquals("subscriber", request.principal().kind());
        assertEquals("oreilly", request.principal().issuer());
        assertEquals("abc123", request.principal().id());
    }

    @Test
    void aRequestWithoutAPrincipalHasNone() throws IOException {
        assertNull(parse("").principal());
    }

    @Test
    void aMalformedPrincipalGivesNone() throws IOException {
        assertNull(parse(",\"principal\":\"subscriber\"").principal());
        assertNull(parse(",\"principal\":{\"id\":\"abc\"}").principal());
        assertNull(parse(",\"principal\":null").principal());
    }
}
