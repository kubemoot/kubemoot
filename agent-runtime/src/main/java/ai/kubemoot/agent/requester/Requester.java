package ai.kubemoot.agent.requester;

import com.fasterxml.jackson.databind.JsonNode;

import java.time.LocalDate;
import java.time.LocalDateTime;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.time.format.DateTimeParseException;
import java.time.temporal.ChronoUnit;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

/**
 * Where and when the person asking is: their IANA time zone and their local date and time,
 * as their client reported them. Tools that care about "today" read this instead of the
 * server's clock, which may be in another time zone. Either part may be missing; a value
 * that is not a known IANA zone or not an ISO local date-time is dropped, so a bad client
 * value behaves like an absent one.
 */
public record Requester(String timeZone, String localDateTime) {

    /** The key under which the requester rides in a thread_start message's metadata. */
    public static final String METADATA_KEY = "requester";
    public static final String FIELD_TIME_ZONE = "timeZone";
    public static final String FIELD_LOCAL_DATE_TIME = "localDateTime";
    /** Only in tool metadata: the requester's date, derived so a tool need not parse a date-time. */
    public static final String FIELD_REQUEST_DATE = "requestDate";

    private static final int MIN_YEAR = 0;
    private static final int MAX_YEAR = 9999;
    private static final Set<String> ZONES = ZoneId.getAvailableZoneIds();

    /** The requester for these client values, or empty when neither is usable. */
    public static Optional<Requester> of(String timeZone, String localDateTime) {
        String zone = validZone(timeZone);
        String local = validLocalDateTime(localDateTime);
        return zone == null && local == null ? Optional.empty() : Optional.of(new Requester(zone, local));
    }

    /** The requester in a JSON object with timeZone and localDateTime fields; empty for anything else. */
    public static Optional<Requester> fromJson(JsonNode node) {
        if (node == null || !node.isObject()) {
            return Optional.empty();
        }
        return of(text(node, FIELD_TIME_ZONE), text(node, FIELD_LOCAL_DATE_TIME));
    }

    /** The requester's date: from their local date-time, else today in their zone, else null. */
    public String requestDate() {
        if (localDateTime != null) {
            return LocalDateTime.parse(localDateTime).toLocalDate().toString();
        }
        return timeZone == null ? null : LocalDate.now(ZoneId.of(timeZone)).toString();
    }

    /** The fields as the bus carries them in thread_start metadata. */
    public Map<String, Object> toBusMetadata() {
        var fields = new LinkedHashMap<String, Object>();
        if (timeZone != null) {
            fields.put(FIELD_TIME_ZONE, timeZone);
        }
        if (localDateTime != null) {
            fields.put(FIELD_LOCAL_DATE_TIME, localDateTime);
        }
        return fields;
    }

    /** The fields as a tool call's MCP {@code _meta}: the bus fields plus requestDate. */
    public Map<String, Object> toolMeta() {
        var fields = toBusMetadata();
        String date = requestDate();
        if (date != null) {
            fields.put(FIELD_REQUEST_DATE, date);
        }
        return fields;
    }

    private static String text(JsonNode node, String field) {
        JsonNode value = node.get(field);
        return value != null && value.isTextual() ? value.asText() : null;
    }

    private static String validZone(String value) {
        if (value == null) {
            return null;
        }
        String zone = value.trim();
        return ZONES.contains(zone) ? zone : null;
    }

    private static String validLocalDateTime(String value) {
        if (value == null) {
            return null;
        }
        try {
            LocalDateTime parsed = LocalDateTime.parse(value.trim());
            if (parsed.getYear() < MIN_YEAR || parsed.getYear() > MAX_YEAR) {
                return null;
            }
            return parsed.truncatedTo(ChronoUnit.SECONDS).format(DateTimeFormatter.ISO_LOCAL_DATE_TIME);
        } catch (DateTimeParseException e) {
            return null;
        }
    }
}
