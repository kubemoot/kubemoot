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

    @Test
    void aFiveDigitYearIsRejected() {
        assertTrue(Requester.of(null, "+12345-01-01T00:00:00").isEmpty());
        assertTrue(Requester.of(null, "-0001-01-01T00:00:00").isEmpty());
    }

    @Test
    void requestDateIsTheDatePartOfTheLocalDateTime() {
        assertEquals("0999-03-04", Requester.of(null, "0999-03-04T10:00").orElseThrow().requestDate());
    }

    // --- The date and zone fixed when the question arrives ---

    private static final java.time.Instant JUST_BEFORE_MIDNIGHT_NEW_YORK = java.time.Instant.parse("2026-11-21T04:59:59Z");
    private static final java.time.Instant JUST_AFTER_MIDNIGHT_NEW_YORK = java.time.Instant.parse("2026-11-21T05:00:01Z");

    @Test
    void theClientsZoneWinsOverTheCrewsDefault() {
        var client = Requester.of("Asia/Tokyo", null).orElseThrow();
        var arrived = Requester.arrived(client, "America/New_York", JUST_BEFORE_MIDNIGHT_NEW_YORK);
        assertEquals("Asia/Tokyo", arrived.timeZone());
        assertEquals("2026-11-21", arrived.requestDate());
    }

    @Test
    void withoutAClientZoneTheCrewsDefaultIsUsed() {
        var arrived = Requester.arrived(null, "America/New_York", JUST_BEFORE_MIDNIGHT_NEW_YORK);
        assertEquals("America/New_York", arrived.timeZone());
        assertEquals("2026-11-20", arrived.requestDate());
    }

    @Test
    void withNeitherTheZoneIsUtcAndNamedAsSuch() {
        var arrived = Requester.arrived(null, null, JUST_BEFORE_MIDNIGHT_NEW_YORK);
        assertEquals("UTC", arrived.timeZone());
        assertEquals("2026-11-21", arrived.requestDate());
    }

    @Test
    void aCrewDefaultThatIsNotAZoneFallsBackToUtc() {
        assertEquals("UTC", Requester.arrived(null, "Mars/Base", JUST_BEFORE_MIDNIGHT_NEW_YORK).timeZone());
        assertEquals("UTC", Requester.arrived(null, "", JUST_BEFORE_MIDNIGHT_NEW_YORK).timeZone());
        assertEquals("UTC", Requester.arrived(null, "  ", JUST_BEFORE_MIDNIGHT_NEW_YORK).timeZone());
    }

    @Test
    void aClientWithOnlyALocalTimeGetsTheDefaultZoneAndKeepsItsLocalTime() {
        var client = Requester.of(null, "2026-11-20T23:00:00").orElseThrow();
        var arrived = Requester.arrived(client, "Europe/Paris", JUST_BEFORE_MIDNIGHT_NEW_YORK);
        assertEquals("Europe/Paris", arrived.timeZone());
        assertEquals("2026-11-20T23:00:00", arrived.localDateTime());
        // the client's local time never drives the date: the arrival instant in the resolved zone does
        assertEquals("2026-11-21", arrived.requestDate());
    }

    // The date is fixed from the arrival instant: a discussion that runs past midnight keeps the date
    // it was asked on, and a question asked after midnight gets the next one.
    @Test
    void theDateIsFixedAtArrivalAcrossAMidnightBoundary() {
        var client = Requester.of("America/New_York", null).orElseThrow();
        var before = Requester.arrived(client, null, JUST_BEFORE_MIDNIGHT_NEW_YORK);
        var after = Requester.arrived(client, null, JUST_AFTER_MIDNIGHT_NEW_YORK);
        assertEquals("2026-11-20", before.requestDate());
        assertEquals("2026-11-21", after.requestDate());
        // the requester travels on the bus as it was fixed; reading it later does not move the date
        var carried = Requester.fromJson(MAPPER.valueToTree(before.toBusMetadata())).orElseThrow();
        assertEquals("2026-11-20", carried.requestDate());
        assertEquals("2026-11-20", carried.toolMeta().get("requestDate"));
    }

    @Test
    void anInvalidRequestDateOnTheBusIsIgnored() {
        var node = MAPPER.createObjectNode().put("timeZone", "Asia/Tokyo").put("requestDate", "tomorrow");
        var requester = Requester.fromJson(node).orElseThrow();
        assertEquals("Asia/Tokyo", requester.timeZone());
        assertEquals(LocalDate.now(ZoneId.of("Asia/Tokyo")).toString(), requester.requestDate());
    }
}
