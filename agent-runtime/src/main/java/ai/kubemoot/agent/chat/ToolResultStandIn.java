package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.nio.charset.StandardCharsets;

/**
 * What a tooler's model sees in place of a tool result too large for its context
 * window. A tooler's contribution is the raw tool output, which the crew receives
 * whole, so its model never needs to read the result: it only judges whether the call
 * returned what it asked for and then stops or calls another tool. The stand-in is
 * built in code from the result alone: the tool name, that the full result is kept
 * for the crew, its size, and a bounded head to judge it by.
 */
final class ToolResultStandIn {

    /** Most lines of the result shown. */
    static final int HEAD_LINES = 20;

    /** Most characters of the result shown, so a single very long line cannot fill the window. */
    static final int HEAD_CHARS = 1_500;

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private ToolResultStandIn() {}

    /** The stand-in for {@code result} of {@code toolName}. */
    static String of(String toolName, String result) {
        String text = result == null ? "" : result;
        StringBuilder out = new StringBuilder();
        out.append('[').append(toolName).append("] The full result is kept for the crew and posted with your output; ")
                .append("it is not shown here because it does not fit the context window.\n");
        out.append("Size: ").append(text.getBytes(StandardCharsets.UTF_8).length).append(" bytes, ")
                .append(lineCount(text)).append(" lines");
        int items = itemCount(text);
        if (items >= 0) {
            out.append(", ").append(items).append(" items");
        }
        out.append(".\nFirst ").append(HEAD_LINES).append(" lines (cut at ").append(HEAD_CHARS)
                .append(" characters):\n").append(head(text)).append('\n');
        out.append("If this is the data you asked for, stop: the crew receives all of it. ")
                .append("Call another tool only for different data.");
        return out.toString();
    }

    /** The first {@link #HEAD_LINES} lines of {@code text}, at most {@link #HEAD_CHARS} characters. */
    static String head(String text) {
        int end = 0;
        for (int lines = 0; lines < HEAD_LINES && end < text.length(); lines++) {
            int nl = text.indexOf('\n', end);
            end = nl < 0 ? text.length() : nl + 1;
        }
        String head = text.substring(0, Math.min(end, text.length()));
        if (head.length() > HEAD_CHARS) {
            head = head.substring(0, HEAD_CHARS);
        }
        return head.stripTrailing();
    }

    /** Number of lines in {@code text}: zero when empty, and a last line without a newline counts. */
    static int lineCount(String text) {
        if (text.isEmpty()) {
            return 0;
        }
        int lines = 1;
        for (int i = text.indexOf('\n'); i >= 0 && i < text.length() - 1; i = text.indexOf('\n', i + 1)) {
            lines++;
        }
        return lines;
    }

    /**
     * The number of items when {@code text} is a JSON array, or a JSON object with an
     * {@code items} array (the shape of a Kubernetes list); -1 for anything else.
     */
    static int itemCount(String text) {
        String t = text.stripLeading();
        if (!t.startsWith("[") && !t.startsWith("{")) {
            return -1;
        }
        try {
            JsonNode root = MAPPER.readTree(t);
            JsonNode list = root.isArray() ? root : root.path("items");
            return list.isArray() ? list.size() : -1;
        } catch (Exception e) {
            return -1;
        }
    }
}
