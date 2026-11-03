package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * What the coordinator takes from analysts for the coverage of a thread's evidence: the keys an
 * agree signal says were read, whether the analyst answered with data, and which analysts the crew has.
 */
final class AnalystReadouts {

    private static final String ROLE_ANALYST = "analyst";
    private static final Pattern THINK_BLOCK = Pattern.compile("(?s)<think>.*?</think>");

    private AnalystReadouts() {}

    /** The keys of the signal's {@code readKeys} metadata; empty when it has none. */
    static Set<String> readKeysOf(JsonNode signal) {
        var keys = new LinkedHashSet<String>();
        signal.path("metadata").path("readKeys").forEach(k -> {
            if (k.isTextual() && !k.asText().isBlank()) {
                keys.add(k.asText());
            }
        });
        return keys;
    }

    /** False when the analyst's answer is only the no-data sentinel (NO_DATA or TOOL_GAP), after thinking is removed. */
    static boolean answeredWithData(String content) {
        String answer = THINK_BLOCK.matcher(content == null ? "" : content).replaceAll("").strip();
        return !answer.isEmpty() && !"NO_DATA".equals(answer) && !"TOOL_GAP".equals(answer);
    }

    /** The resumes of the crew's analysts that have a name, in resume order; the one parser of that list. */
    static List<JsonNode> analysts(JsonNode resumes) {
        var nodes = new ArrayList<JsonNode>();
        if (resumes != null && resumes.isArray()) {
            for (JsonNode node : resumes) {
                if (ROLE_ANALYST.equals(node.path("role").asText("")) && !node.path("name").asText("").isEmpty()) {
                    nodes.add(node);
                }
            }
        }
        return nodes;
    }
}
