package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * What the coordinator takes from analysts for the coverage of a thread's evidence: the keys an
 * agree signal says were read, whether the analyst answered with data, which analysts of the crew can
 * reduce stored results with a tool, and the message that convenes them for a second review.
 */
final class AnalystReadouts {

    /** The tools that reduce stored results: a query over them, or code run over their files. */
    static final List<String> QUERY_TOOLS = List.of("workspace_query", "execute_code");

    /** The review_ready metadata field that holds each convened analyst's whole task, by analyst name. */
    static final String FIELD_TASKS = "tasks";

    private static final String ROLE_ANALYST = "analyst";
    private static final Pattern THINK_BLOCK = Pattern.compile("(?s)<think>.*?</think>");
    private static final String REVIEW_TEMPLATE = RefinePrompts.load("review-second.adl");

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

    /** The analysts in the crew's resumes whose tools include one that runs a query or code, in resume order. */
    static List<String> queryCapable(JsonNode resumes) {
        return analysts(resumes).stream().filter(n -> hasQueryTool(n.path("tools")))
                .map(n -> n.path("name").asText("")).toList();
    }

    private static boolean hasQueryTool(JsonNode tools) {
        for (JsonNode tool : tools) {
            if (QUERY_TOOLS.stream().anyMatch(q -> tool.asText("").contains(q))) {
                return true;
            }
        }
        return false;
    }

    /**
     * The task of each convened analyst, by analyst: the question, the key patterns it is to read, the
     * stored calls of each pattern that no analyst read (the catalog lines, with the arguments of each
     * call), and how a pattern reads as one table. The message is the analyst's whole task, so it carries
     * no other part of the thread.
     */
    static Map<String, String> secondReviewTasks(String question, List<DeliveryPlan.Assignment> assignments,
                                                 List<ThreadCatalog.Entry> uncovered) {
        var tasks = new LinkedHashMap<String, String>();
        for (DeliveryPlan.Assignment a : assignments) {
            tasks.put(a.analyst(), RefinePrompts.render(REVIEW_TEMPLATE, Map.of(
                    "question", question == null ? "" : question.strip(),
                    "assignments", assignmentLines(a, uncovered),
                    "reading", ThreadCatalog.READING_NOTE)));
        }
        return tasks;
    }

    /** The review_ready content of a second review: the tasks, one after the other. */
    static String secondReviewContent(Map<String, String> tasks) {
        return String.join("\n\n", tasks.values());
    }

    /** Per pattern of the assignment: its line, then the lines of the pattern's calls that no analyst read. */
    private static String assignmentLines(DeliveryPlan.Assignment a, List<ThreadCatalog.Entry> uncovered) {
        var lines = new ArrayList<String>();
        for (Map.Entry<String, Integer> p : a.patterns().entrySet()) {
            lines.add("[ASSIGN analyst=" + a.analyst() + " pattern=" + p.getKey() + " calls=" + p.getValue() + "]");
            uncovered.stream().filter(e -> !e.error() && EvidenceCoverage.matches(p.getKey(), e.key()))
                    .forEach(e -> lines.add(ThreadCatalog.callLine(e)));
        }
        return String.join("\n", lines);
    }
}
