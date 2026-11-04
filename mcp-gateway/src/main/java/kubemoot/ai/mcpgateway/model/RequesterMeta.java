package kubemoot.ai.mcpgateway.model;

import java.time.DateTimeException;
import java.time.LocalDate;
import java.time.LocalDateTime;
import java.time.ZoneId;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Set;
import java.util.function.Predicate;
import java.util.regex.Pattern;

/**
 * The part of a caller's MCP {@code _meta} the gateway forwards to a tool server: the
 * requester's {@code timeZone}, {@code localDateTime} and {@code requestDate}, and the
 * {@code workspacePrefix} of the discussion thread the call runs in (three key parts,
 * {@code namespace/crew/thread/}, which the artifact tools use to read a short key inside
 * that thread), each re-validated and length-capped. Every other key, and any value that
 * fails its check, is dropped, so a caller cannot use {@code _meta} to smuggle other fields
 * to a tool.
 */
public final class RequesterMeta {

    static final int MAX_ZONE_LENGTH = 64;
    static final int MAX_LOCAL_DATE_TIME_LENGTH = 32;
    static final int MAX_DATE_LENGTH = 10;
    static final int MAX_WORKSPACE_PREFIX_LENGTH = 512;

    private static final Set<String> ZONES = ZoneId.getAvailableZoneIds();
    private static final Pattern LOCAL_DATE_TIME =
            Pattern.compile("\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}(:\\d{2}(\\.\\d{1,9})?)?");
    private static final Pattern DATE = Pattern.compile("\\d{4}-\\d{2}-\\d{2}");
    private static final Pattern WORKSPACE_PREFIX =
            Pattern.compile("[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/");

    private RequesterMeta() {
    }

    /** The forwardable subset of {@code raw}; empty when it is not a map or holds nothing valid. */
    public static Map<String, Object> sanitize(Object raw) {
        var kept = new LinkedHashMap<String, Object>();
        if (raw instanceof Map<?, ?> map) {
            keep(kept, map, "timeZone", RequesterMeta::validZone);
            keep(kept, map, "localDateTime", RequesterMeta::validLocalDateTime);
            keep(kept, map, "requestDate", RequesterMeta::validDate);
            keep(kept, map, "workspacePrefix", RequesterMeta::validWorkspacePrefix);
        }
        return kept;
    }

    private static void keep(Map<String, Object> kept, Map<?, ?> source, String key, Predicate<String> valid) {
        if (source.get(key) instanceof String value && valid.test(value)) {
            kept.put(key, value);
        }
    }

    private static boolean validZone(String value) {
        return value.length() <= MAX_ZONE_LENGTH && ZONES.contains(value);
    }

    private static boolean validLocalDateTime(String value) {
        if (value.length() > MAX_LOCAL_DATE_TIME_LENGTH || !LOCAL_DATE_TIME.matcher(value).matches()) {
            return false;
        }
        try {
            LocalDateTime.parse(value);
            return true;
        } catch (DateTimeException e) {
            return false;
        }
    }

    private static boolean validWorkspacePrefix(String value) {
        if (value.length() > MAX_WORKSPACE_PREFIX_LENGTH || !WORKSPACE_PREFIX.matcher(value).matches()) {
            return false;
        }
        for (String part : value.split("/")) {
            if (".".equals(part) || "..".equals(part)) {
                return false;
            }
        }
        return true;
    }

    private static boolean validDate(String value) {
        if (value.length() > MAX_DATE_LENGTH || !DATE.matcher(value).matches()) {
            return false;
        }
        try {
            LocalDate.parse(value);
            return true;
        } catch (DateTimeException e) {
            return false;
        }
    }
}
