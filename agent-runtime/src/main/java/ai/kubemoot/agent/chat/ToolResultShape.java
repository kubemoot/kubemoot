package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

/**
 * What a tool result is, read once: JSON or text, and how many rows. A result is JSON when it
 * is a JSON object or list that parses; its row count is the list length, or the length of the
 * {@code items} list of an object (a Kubernetes list). Any other result is text with an unknown
 * count: telling a kubectl table from a log is the workspace query's job, which reads the text.
 */
record ToolResultShape(boolean json, int rows) {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    static ToolResultShape of(String result) {
        String t = result == null ? "" : result.strip();
        if (!t.startsWith("[") && !t.startsWith("{")) {
            return new ToolResultShape(false, -1);
        }
        try {
            JsonNode root = MAPPER.readTree(t);
            JsonNode list = root.isArray() ? root : root.path("items");
            return new ToolResultShape(true, list.isArray() ? list.size() : -1);
        } catch (Exception e) {
            return new ToolResultShape(false, -1);
        }
    }
}
