package ai.kubemoot.agent.requester;

/**
 * The one line of context every agent's turn starts with: the asker's date and zone, and where
 * the agent runs. The date is the one fixed when the question arrived (never the clock time), so
 * the line is the same for every turn of a discussion and a prompt cache holds across them. The
 * namespace and crew come from the runtime, so a prompt does not have to carry them.
 *
 * <p>The sidecar builds the same line for harness agents (agent-sidecar, a2a.ContextLine); both
 * are pinned by tests on the same strings, so change them together.
 */
public final class TurnContext {

    private TurnContext() {
    }

    /**
     * The line, for example {@code Today is 2026-11-20 (America/New_York). You are in namespace
     * crew-homelab-pilot, crew homelab-pilot.} A part that is not known is left out; empty when
     * nothing is known.
     */
    public static String line(Requester requester, String namespace, String crew) {
        var line = new StringBuilder();
        String date = requester == null ? null : requester.requestDate();
        if (date != null) {
            line.append("Today is ").append(date);
            if (requester.timeZone() != null) {
                line.append(" (").append(requester.timeZone()).append(')');
            }
            line.append('.');
        }
        String place = place(namespace, crew);
        if (!place.isEmpty()) {
            line.append(line.length() > 0 ? " " : "").append("You are in ").append(place).append('.');
        }
        return line.toString();
    }

    /** The system prompt followed by the line, or the prompt alone when the line is empty. */
    public static String appendTo(String systemPrompt, String line) {
        String prompt = systemPrompt == null ? "" : systemPrompt;
        if (line == null || line.isEmpty()) {
            return prompt;
        }
        return prompt.isEmpty() ? line : prompt + "\n\n" + line;
    }

    private static String place(String namespace, String crew) {
        boolean hasNamespace = namespace != null && !namespace.isBlank();
        boolean hasCrew = crew != null && !crew.isBlank();
        if (hasNamespace && hasCrew) {
            return "namespace " + namespace + ", crew " + crew;
        }
        if (hasNamespace) {
            return "namespace " + namespace;
        }
        return hasCrew ? "crew " + crew : "";
    }
}
