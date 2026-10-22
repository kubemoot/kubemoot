package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.ArrayList;
import java.util.Collection;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * The coordinator's one-line statement of which agents it convened and why. The reason
 * for each agent is the first sentence of its description in the crew capability
 * catalog, the same text the coordinator selected from. Parsed with readTree only,
 * never records or readValue (GraalVM native).
 */
final class SubcommitteeLine {

    static final String MESSAGE_TYPE = "subcommittee";
    static final String FIELD_CONVENED = "convened";
    private static final int REASON_MAX_CHARS = 120;
    private static final String NO_REASON = "";

    /** One convened agent and its short reason; the reason is empty when the catalog has none. */
    record Member(String name, String reason) {}

    private SubcommitteeLine() {}

    /** The members for {@code names}, in order, with reasons from the raw crew resumes JSON. */
    static List<Member> members(ObjectMapper mapper, Collection<String> names, String rawResumes) {
        var descriptions = descriptionsByName(mapper, rawResumes);
        var members = new ArrayList<Member>();
        for (var name : names) {
            if (name == null || name.isBlank()) continue;
            members.add(new Member(name, shortReason(descriptions.get(name))));
        }
        return members;
    }

    /** The sentence the dashboard shows: "Convened 2 agents: a (reason), b (reason)". */
    static String content(List<Member> members) {
        var parts = new ArrayList<String>();
        for (var m : members) {
            parts.add(m.reason().isEmpty() ? m.name() : m.name() + " (" + m.reason() + ")");
        }
        String noun = members.size() == 1 ? "agent" : "agents";
        return "Convened " + members.size() + " " + noun + ": " + String.join(", ", parts);
    }

    /** The metadata map for the bus message: {@code convened: [{name, reason}]}. */
    static Map<String, Object> metadata(List<Member> members) {
        var list = new ArrayList<Map<String, Object>>();
        for (var m : members) {
            list.add(Map.of("name", m.name(), "reason", m.reason()));
        }
        var metadata = new HashMap<String, Object>();
        metadata.put(FIELD_CONVENED, list);
        return metadata;
    }

    /** The first sentence of {@code description}, cut to a short line; empty when there is none. */
    static String shortReason(String description) {
        if (description == null) return NO_REASON;
        String text = description.strip().replaceAll("\\s+", " ");
        int stop = text.indexOf(". ");
        if (stop > 0) text = text.substring(0, stop + 1);
        if (text.endsWith(".")) text = text.substring(0, text.length() - 1);
        if (text.length() > REASON_MAX_CHARS) {
            text = text.substring(0, REASON_MAX_CHARS - 3).stripTrailing() + "...";
        }
        return text;
    }

    private static Map<String, String> descriptionsByName(ObjectMapper mapper, String rawResumes) {
        var result = new HashMap<String, String>();
        if (rawResumes == null || rawResumes.isBlank()) return result;
        try {
            JsonNode arr = mapper.readTree(rawResumes);
            if (!arr.isArray()) return result;
            for (var node : arr) {
                String name = node.path("name").asText("");
                if (!name.isEmpty()) result.put(name, node.path("description").asText(""));
            }
        } catch (Exception e) {
            return new HashMap<>();
        }
        return result;
    }
}
