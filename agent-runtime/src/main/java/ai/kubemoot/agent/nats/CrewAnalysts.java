package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.List;

/** The analysts of a crew, read from the crew's resumes. */
final class CrewAnalysts {

    private CrewAnalysts() {}

    /** The resumes of the crew's analysts that have a name, in resume order; the one parser of that list. */
    static List<JsonNode> analysts(JsonNode resumes) {
        var nodes = new ArrayList<JsonNode>();
        if (resumes != null && resumes.isArray()) {
            for (JsonNode node : resumes) {
                if (CrewRoles.ANALYST.equals(node.path("role").asText("")) && !node.path("name").asText("").isEmpty()) {
                    nodes.add(node);
                }
            }
        }
        return nodes;
    }
}
