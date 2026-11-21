package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.TextCut;
import java.util.ArrayList;
import java.util.Map;

/**
 * The answer shown when the synthesis call fails: each agent's finding, then the stored results of
 * the tool calls it made, so a finding whose data was spilled to the workspace still reaches the
 * person. The room is that of the synthesis input ({@link StoredResults.Source}): one result up to
 * its share, one agent's results up to a contribution's share, all of them up to the total. A
 * result that is cut says so and names the key it is stored under; an agent with neither a finding
 * nor a stored result says it returned no data, never an empty entry.
 */
final class FallbackFindings {

    /** The most characters of one agent's finding text. */
    static final int FINDING_CHARS = 2000;

    /** What an agent with no finding text and no stored result is shown as. */
    static final String NO_DATA = "returned no data";

    private FallbackFindings() {}

    /** The entries for {@code findings} (agent name to its finding text), in the order given. */
    static String render(Map<String, String> findings, StoredResults.Source source) {
        var total = new Room(source.total());
        var out = new StringBuilder();
        for (var finding : findings.entrySet()) {
            out.append("**").append(finding.getKey()).append("**: ")
                    .append(entry(finding.getKey(), finding.getValue(), source, total)).append("\n\n");
        }
        return out.toString();
    }

    private static String entry(String agent, String finding, StoredResults.Source source, Room total) {
        var parts = new ArrayList<String>();
        String text = ManifestText.strip(finding == null ? "" : finding, true).strip();
        if (!text.isEmpty()) {
            parts.add(TextCut.truncate(text, FINDING_CHARS));
        }
        int agentLeft = source.perAgent();
        for (var call : source.calls()) {
            if (call.agent().equals(agent)) {
                String block = resultBlock(call, source, Math.min(source.perResult(), Math.min(agentLeft, total.left)));
                agentLeft -= block.length();
                total.left -= block.length();
                parts.add(block);
            }
        }
        return parts.isEmpty() ? NO_DATA : String.join("\n\n", parts);
    }

    private static String resultBlock(ThreadCatalog.Entry call, StoredResults.Source source, int room) {
        String key = call.relativeKey(source.prefix());
        String head = "Result of " + call.tool() + ":\n";
        if (room < StoredResults.MIN_SHOWN_CHARS || source.timeUp().getAsBoolean()) {
            return head + "(not shown, no room left in this answer; the full result is stored in the workspace at "
                    + key + ")";
        }
        var clipped = StoredResults.readClipped(source.reader(), call, room, source.timeUp());
        if (clipped.text() == null) {
            return head + "(could not be read; the stored result is in the workspace at " + key + ")";
        }
        if (clipped.refused()) {
            return head + "(not shown, its content could not be shown safely; the full result is stored in the "
                    + "workspace at " + key + ")";
        }
        return head + clipped.text() + (clipped.cut()
                ? "\n[truncated: showing the first " + clipped.text().length() + " characters; the full result is "
                        + "stored in the workspace at " + key + "]"
                : "");
    }

    /** The characters of stored results left to show across all agents. */
    private static final class Room {
        int left;

        Room(int left) {
            this.left = left;
        }
    }
}
