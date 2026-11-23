package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.util.Collection;
import java.util.List;
import java.util.TreeSet;

/**
 * What a person reads about a source that could not be reached: the declared description of the
 * agent that failed (its triage summary, else its description), taken from the crew's resumes.
 * Tool names, agent names and keys stay in the failure signal, the logs and the dashboard.
 */
final class FailedSourceWording {

    /** Longest phrase shown for one source, cut at a word boundary. */
    static final int MAX_PHRASE_CHARS = 110;

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private FailedSourceWording() {}

    /**
     * The plain-words phrases for the failed agents, sorted and without repeats. An agent with no
     * resume or no description adds nothing; unparseable resumes give an empty list.
     */
    static List<String> phrases(String resumesJson, Collection<String> failedAgents) {
        if (resumesJson == null || resumesJson.isBlank() || failedAgents == null || failedAgents.isEmpty()) {
            return List.of();
        }
        var out = new TreeSet<String>();
        for (JsonNode node : resumes(resumesJson)) {
            if (failedAgents.contains(node.path("name").asText(""))) {
                String phrase = phrase(node);
                if (!phrase.isEmpty()) {
                    out.add(phrase);
                }
            }
        }
        return List.copyOf(out);
    }

    /** The entries of a resumes array; empty when the text is not one. */
    private static Iterable<JsonNode> resumes(String resumesJson) {
        try {
            JsonNode root = MAPPER.readTree(resumesJson);
            return root != null && root.isArray() ? root : List.of();
        } catch (Exception e) {
            return List.of();
        }
    }

    /** The first sentence of the resume's triage summary, else its description, capped in length. */
    static String phrase(JsonNode resume) {
        String text = resume.path("triageSummary").asText("").isBlank()
                ? resume.path("description").asText("") : resume.path("triageSummary").asText("");
        text = text.replaceAll("\\s+", " ").strip();
        int end = text.indexOf(". ");
        if (end > 0) {
            text = text.substring(0, end);
        }
        text = stripTrailing(text);
        if (text.length() > MAX_PHRASE_CHARS) {
            int cut = text.lastIndexOf(' ', MAX_PHRASE_CHARS);
            text = stripTrailing(text.substring(0, cut > 0 ? cut : MAX_PHRASE_CHARS)) + "...";
        }
        return text;
    }

    private static String stripTrailing(String text) {
        return text.replaceAll("[\\s.,;:]+$", "");
    }
}
