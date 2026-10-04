package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;

/**
 * Reads text fields from discussion messages parsed with readTree (no reflection,
 * so it is safe in the native image).
 */
final class JsonFields {

    private JsonFields() {
    }

    /** The field's text, or "" when the message does not carry the field. */
    static String text(JsonNode msg, String field) {
        return text(msg, field, "");
    }

    /** The field's text, or the fallback when the message does not carry the field. */
    static String text(JsonNode msg, String field, String fallback) {
        return msg.has(field) ? msg.get(field).asText() : fallback;
    }
}
