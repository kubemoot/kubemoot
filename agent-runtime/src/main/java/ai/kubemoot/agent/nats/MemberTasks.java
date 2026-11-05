package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * What the coordinator asked of each convened member: one task per member, the shared brief
 * a member without a task falls back to, and the bookkeeping the event log records.
 *
 * <p>Read from the coordinator's JSON with {@code JsonNode} only, never record binding,
 * which fails silently in the native image.
 *
 * @param tasks         task text by member name; only convened members with a non-blank task
 * @param sharedBrief   the brief every member without a task receives
 * @param fallbacks     convened members without a task, sorted
 * @param ignored       names given a task that are not convened members, sorted
 */
record MemberTasks(Map<String, String> tasks, String sharedBrief, List<String> fallbacks, List<String> ignored) {

    /** The field of the coordinator's JSON that holds the tasks. */
    static final String FIELD = "memberTasks";

    /** The field that holds the brief a member without a task receives. */
    static final String SHARED_BRIEF = "sharedBrief";

    /** No per-member tasks: every member reads the shared brief, as before. */
    static final MemberTasks NONE = new MemberTasks(Map.of(), "", List.of(), List.of());

    MemberTasks {
        tasks = Collections.unmodifiableMap(new TreeMap<>(tasks));
        sharedBrief = sharedBrief == null ? "" : sharedBrief;
        fallbacks = List.copyOf(fallbacks);
        ignored = List.copyOf(ignored);
    }

    /**
     * The tasks in {@code coordinatorJson} for the convened {@code members}. A missing or
     * malformed field gives no tasks (every member falls back); a blank or null task counts
     * as none; a name that is not a member is dropped and recorded.
     */
    static MemberTasks parse(JsonNode coordinatorJson, Collection<String> members, String sharedBrief) {
        JsonNode node = coordinatorJson == null ? null : coordinatorJson.get(FIELD);
        Map<String, String> tasks = new TreeMap<>();
        List<String> ignored = new ArrayList<>();
        if (node != null && node.isObject()) {
            node.fields().forEachRemaining(entry -> sort(entry.getKey(), entry.getValue(), members, tasks, ignored));
        }
        List<String> fallbacks = members.stream().filter(m -> !tasks.containsKey(m)).sorted().toList();
        return new MemberTasks(tasks, sharedBrief, fallbacks, ignored.stream().sorted().toList());
    }

    private static void sort(String name, JsonNode value, Collection<String> members,
                             Map<String, String> tasks, List<String> ignored) {
        if (!members.contains(name)) {
            ignored.add(name);
            return;
        }
        String text = value.isTextual() ? value.asText().trim() : "";
        if (!text.isEmpty()) {
            tasks.put(name, text);
        }
    }

    boolean hasTasks() {
        return !tasks.isEmpty();
    }

    /** What the member reads in place of the thread's advisory text: its own task, else the shared brief. */
    String forMember(String member) {
        return tasks.getOrDefault(member, sharedBrief);
    }

    /** One text for display: the shared brief, or each member's task on its own line when there is no brief. */
    String combined() {
        if (!sharedBrief.isBlank() || tasks.isEmpty()) {
            return sharedBrief;
        }
        var sb = new StringBuilder();
        tasks.forEach((member, task) -> sb.append(sb.isEmpty() ? "" : "\n").append(member).append(": ").append(task));
        return sb.toString();
    }

    /** Puts the tasks and their bookkeeping on an event's data; nothing when the coordinator gave none to look at. */
    void writeTo(ObjectNode data, boolean reasoned) {
        if (!reasoned) {
            return;
        }
        if (hasTasks()) {
            data.put(SHARED_BRIEF, sharedBrief);
            ObjectNode out = data.putObject(FIELD);
            tasks.forEach(out::put);
        }
        var fell = data.putArray("taskFallbacks");
        fallbacks.forEach(fell::add);
        if (!ignored.isEmpty()) {
            var ign = data.putArray("ignoredTasks");
            ignored.forEach(ign::add);
        }
    }

    /** The tasks as an event recorded them; the event's brief is the combined text and is not read here. */
    static MemberTasks fromEvent(JsonNode data) {
        Map<String, String> tasks = new TreeMap<>();
        JsonNode node = data.path(FIELD);
        if (node.isObject()) {
            node.fields().forEachRemaining(e -> tasks.put(e.getKey(), e.getValue().asText()));
        }
        List<String> fallbacks = new ArrayList<>();
        data.path("taskFallbacks").forEach(n -> fallbacks.add(n.asText()));
        List<String> ignored = new ArrayList<>();
        data.path("ignoredTasks").forEach(n -> ignored.add(n.asText()));
        return new MemberTasks(tasks, data.path(SHARED_BRIEF).asText(""), fallbacks, ignored);
    }
}
