package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * The data gatherers of a crew, read from the crew's resumes, and the rule that a
 * subcommittee is never analysts alone. An analyst computes or reasons over data other
 * agents gathered; with no gatherer beside it, it has nothing to work on. A gatherer is
 * an agent whose declared role is "tooler" (or one that declares none): its contribution is the data itself.
 */
final class CrewDataGatherers {

    /** Why gatherers were added to a subcommittee; recorded on the subcommittee_convened event. */
    static final String REASON_ANALYSTS_ONLY = "analysts_only";

    /**
     * The most gatherers added to a subcommittee. Two covers a question that spans two
     * sources (capacity from one, usage from another) and stays a subcommittee: more would
     * wake agents the question does not need, each a model call on a scarce accelerator.
     */
    static final int MAX_ADDED = 2;

    private CrewDataGatherers() {}

    /** The names of the crew's gatherers, in resume order. Skills and nameless entries are not agents. */
    static List<String> gatherers(JsonNode resumes) {
        var names = new ArrayList<String>();
        if (resumes == null || !resumes.isArray()) {
            return names;
        }
        for (JsonNode node : resumes) {
            String name = node.path("name").asText("");
            if (!name.isEmpty() && !DiscussionOrchestrator.isSkillNode(node) && isGatherer(node)) {
                names.add(name);
            }
        }
        return names;
    }

    private static boolean isGatherer(JsonNode node) {
        return CrewRoles.gathersData(node.path("role").asText(""));
    }

    /** True when the subcommittee has members and every one of them is an analyst. */
    static boolean analystsOnly(Set<String> members, Set<String> analysts) {
        return members != null && !members.isEmpty() && analysts != null && analysts.containsAll(members);
    }

    /**
     * The gatherers to add: those the ranking puts first (best match first), then any it
     * did not rank in resume order, at most {@code max}. With no ranking, resume order.
     */
    static List<String> choose(List<String> ranked, List<String> gatherers, int max) {
        var chosen = new LinkedHashSet<String>();
        if (ranked != null) {
            ranked.stream().filter(gatherers::contains).forEach(chosen::add);
        }
        chosen.addAll(gatherers);
        return chosen.stream().limit(max).toList();
    }
}
