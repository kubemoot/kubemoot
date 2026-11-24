package ai.kubemoot.agent.requester;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Optional;
import java.util.regex.Pattern;

/**
 * Who asked, as a door of the crew verified it: kind, issuer, a pseudonymous id and an optional
 * label. The coordinator copies it from the request onto thread_start; this runtime only reads it
 * there. It scopes tool calls and enters a prompt only when the crew exposes the caller (see
 * {@link TurnContext#callerSentence}). A value that is not well formed reads as absent.
 */
public record Principal(String kind, String issuer, String id, String displayName) {

    /** The key under which the principal rides on a request and in a thread_start message's metadata. */
    public static final String METADATA_KEY = "principal";
    public static final String FIELD_KIND = "kind";
    public static final String FIELD_ISSUER = "issuer";
    public static final String FIELD_ID = "id";
    public static final String FIELD_DISPLAY_NAME = "displayName";
    /** The kind of a caller no door verified. */
    public static final String KIND_ANONYMOUS = "anonymous";

    /** The longest value of any field; a keyed hash in hex is 64 characters. */
    static final int MAX_FIELD_CHARS = 256;

    private static final Pattern PRINTABLE = Pattern.compile("[\\x21-\\x7e]+");

    /** The principal in a JSON object, or empty for anything that is not a well-formed one. */
    public static Optional<Principal> fromJson(JsonNode node) {
        if (node == null || !node.isObject()) {
            return Optional.empty();
        }
        String kind = text(node, FIELD_KIND);
        String issuer = text(node, FIELD_ISSUER);
        String id = text(node, FIELD_ID);
        String name = text(node, FIELD_DISPLAY_NAME);
        if (kind == null || malformed(node, FIELD_ISSUER, issuer) || malformed(node, FIELD_ID, id)) {
            return Optional.empty();
        }
        return Optional.of(new Principal(kind, issuer, id, name));
    }

    /** True when the object names the field but its value is not usable, so the whole principal is refused. */
    private static boolean malformed(JsonNode node, String field, String value) {
        return value == null && node.has(field) && !node.get(field).isNull();
    }

    /** The fields as the bus carries them. */
    public Map<String, Object> toBusMetadata() {
        var fields = new LinkedHashMap<String, Object>();
        fields.put(FIELD_KIND, kind);
        putIfPresent(fields, FIELD_ISSUER, issuer);
        putIfPresent(fields, FIELD_ID, id);
        putIfPresent(fields, FIELD_DISPLAY_NAME, displayName);
        return fields;
    }

    /** The id is pseudonymous, but it names a person's conversations: keep it out of default logs. */
    @Override
    public String toString() {
        return "Principal[kind=" + kind + ", issuer=" + issuer + "]";
    }

    private static void putIfPresent(Map<String, Object> fields, String key, String value) {
        if (value != null) {
            fields.put(key, value);
        }
    }

    private static String text(JsonNode node, String field) {
        JsonNode value = node.get(field);
        if (value == null || !value.isTextual()) {
            return null;
        }
        String text = value.asText();
        boolean ok = text.length() <= MAX_FIELD_CHARS && PRINTABLE.matcher(text).matches();
        return ok ? text : null;
    }
}
