package ai.kubemoot.agent.requester;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.time.LocalDate;
import java.time.ZoneId;
import java.util.Map;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class RequesterTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void aZoneAndALocalDateTimeGiveAToolMetaWithTheRequestersDate() {
        var requester = Requester.of("America/Los_Angeles", "2026-10-24T09:15:30").orElseThrow();
        assertEquals(Map.of("timeZone", "America/Los_Angeles", "localDateTime", "2026-10-24T09:15:30",
                "requestDate", "2026-10-24"), requester.toolMeta());
    }

    @Test
    void theLocalDateIsNotTheServersDate() {
        var requester = Requester.of("Pacific/Kiritimati", "2001-02-03T23:59:00").orElseThrow();
        assertEquals("2001-02-03", requester.requestDate());
    }

    @Test
    void aZoneAloneGivesTodayInThatZone() {
        var requester = Requester.of("Asia/Tokyo", null).orElseThrow();
        assertEquals(LocalDate.now(ZoneId.of("Asia/Tokyo")).toString(), requester.requestDate());
        assertEquals(Map.of("timeZone", "Asia/Tokyo"), requester.toBusMetadata());
    }

    @Test
    void aLocalDateTimeAloneIsKept() {
        var requester = Requester.of(null, "2026-10-24T09:15").orElseThrow();
        assertEquals("2026-10-24T09:15:00", requester.localDateTime());
        assertEquals("2026-10-24", requester.toolMeta().get("requestDate"));
    }

    @Test
    void nothingGivesNoRequester() {
        assertTrue(Requester.of(null, null).isEmpty());
        assertTrue(Requester.of("", "  ").isEmpty());
    }

    @Test
    void anInvalidZoneIsDroppedButAValidDateTimeStays() {
        var requester = Requester.of("Mars/Olympus_Mons", "2026-10-24T09:15:00").orElseThrow();
        assertEquals(null, requester.timeZone());
        assertEquals("2026-10-24", requester.requestDate());
    }

    @Test
    void invalidValuesOfBothKindsGiveNoRequester() {
        assertTrue(Requester.of("+05:00", "yesterday").isEmpty());
        assertTrue(Requester.of("not a zone", "2026-13-45T99:00:00").isEmpty());
        assertEquals("America/New_York", Requester.of(" America/New_York\n", "2026-10-24").orElseThrow().timeZone());
    }

    @Test
    void jsonThatIsNotAnObjectGivesNoRequester() throws Exception {
        assertTrue(Requester.fromJson(null).isEmpty());
        assertTrue(Requester.fromJson(MAPPER.readTree("\"America/New_York\"")).isEmpty());
        assertTrue(Requester.fromJson(MAPPER.readTree("{\"timeZone\": 5, \"localDateTime\": []}")).isEmpty());
    }

    @Test
    void jsonWithBothFieldsGivesTheRequester() throws Exception {
        var node = MAPPER.readTree("{\"timeZone\":\"Europe/Paris\",\"localDateTime\":\"2026-10-24T18:00:00\"}");
        assertEquals("Europe/Paris", Requester.fromJson(node).orElseThrow().timeZone());
    }
}
