package ai.kubemoot.agent.chat;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.HashMap;
import java.util.Map;
import java.util.TreeSet;

/**
 * Counts identical tool calls (same tool, same arguments) within one evaluation. A
 * call is run at most {@link #MAX_IDENTICAL_CALLS} times: the first run, and one
 * more in case the model meant it. A model that sends the same call a third time is
 * not going to get a different answer, so the call is refused with a message that
 * says what to change. Argument key order and whitespace do not make a call different.
 */
final class ToolCallRepeatGuard {

    /** Times one identical call may run in an evaluation. */
    static final int MAX_IDENTICAL_CALLS = 2;

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final Map<String, Integer> counts = new HashMap<>();

    /** Record a call; true when it has already run {@link #MAX_IDENTICAL_CALLS} times and must be refused. */
    boolean refuses(String toolName, String arguments) {
        int seen = counts.merge(toolName + "\n" + canonical(arguments), 1, Integer::sum);
        return seen > MAX_IDENTICAL_CALLS;
    }

    /** The tool error the model gets in place of a refused call. */
    static String refusal(String toolName) {
        return ToolResultBudget.errorJson("You already called " + toolName + " " + MAX_IDENTICAL_CALLS
                + " times with these exact arguments and the result will not change. Change the arguments"
                + " (a namespace, a label selector, a field selection, a limit) or answer from what you have.");
    }

    /** The arguments as JSON with object keys sorted and no whitespace; the trimmed text when not JSON. */
    static String canonical(String arguments) {
        if (arguments == null || arguments.isBlank()) {
            return "";
        }
        try {
            StringBuilder out = new StringBuilder();
            append(MAPPER.readTree(arguments), out);
            return out.toString();
        } catch (com.fasterxml.jackson.core.JsonProcessingException e) {
            return arguments.strip();
        }
    }

    private static void append(JsonNode node, StringBuilder out) {
        if (node.isObject()) {
            var names = new TreeSet<String>();
            node.fieldNames().forEachRemaining(names::add);
            out.append('{');
            for (String name : names) {
                out.append(name).append(':');
                append(node.get(name), out);
                out.append(',');
            }
            out.append('}');
        } else if (node.isArray()) {
            out.append('[');
            for (JsonNode item : node) {
                append(item, out);
                out.append(',');
            }
            out.append(']');
        } else {
            out.append(node.toString());
        }
    }
}
