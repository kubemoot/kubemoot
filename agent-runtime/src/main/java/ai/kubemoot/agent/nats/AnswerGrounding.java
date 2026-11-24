package ai.kubemoot.agent.nats;

import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Whether the names an answer states exist in the data the crew gathered. The completeness
 * contract of the synthesis catches a draft that leaves a listed name out; this catches the
 * opposite, a draft that writes a name no tool returned (a synthesizer that saw only descriptions
 * of stored files, or a cut result, and filled the list in with {@code crew-node-01}).
 *
 * <p>A name here is an instance name: lowercase words joined by hyphens with a number segment
 * ({@code worker-3}, {@code homelab-k8s-1-cp}, {@code crew-node-01}). That shape is what a model
 * invents when it enumerates; plain hyphenated words ({@code read-only}, {@code kube-system}) are
 * not checked. The check is code, from the text alone: a name is grounded when it occurs, in any
 * letter case, in the question and discussion the synthesizer read or in a stored tool result.
 */
public final class AnswerGrounding {

    /** Lowercase words joined by hyphens, standing alone (not part of a path, host or longer word). */
    private static final Pattern HYPHENATED = Pattern.compile("(?<![\\w./:-])[a-z][a-z0-9]*(?:-[a-z0-9]+)+(?![\\w-])");

    /** A segment that is only digits. */
    private static final Pattern NUMBER_SEGMENT = Pattern.compile("(?:^|-)\\d+(?:-|$)");

    /** Most names listed in a re-prompt or a caveat; the rest are counted. */
    static final int MAX_LISTED = 10;

    private AnswerGrounding() {}

    /** The instance names {@code text} states, once each, in order. */
    static List<String> namesIn(String text) {
        if (text == null) {
            return List.of();
        }
        Set<String> names = new LinkedHashSet<>();
        Matcher m = HYPHENATED.matcher(text);
        while (m.find()) {
            if (NUMBER_SEGMENT.matcher(m.group()).find()) {
                names.add(m.group());
            }
        }
        return new ArrayList<>(names);
    }

    /** The names of {@code draft} that occur in none of {@code sources}. */
    static List<String> ungrounded(String draft, List<String> sources) {
        return stillMissing(namesIn(draft), sources);
    }

    /** The {@code names} that occur in none of {@code sources}, in any letter case. */
    static List<String> stillMissing(List<String> names, List<String> sources) {
        List<String> missing = new ArrayList<>(names);
        for (String source : sources) {
            if (missing.isEmpty()) {
                break;
            }
            String lower = source == null ? "" : source.toLowerCase(Locale.ROOT);
            missing.removeIf(lower::contains);
        }
        return missing;
    }

    /** The re-prompt: the input, the draft and the names no gathered data holds. */
    static String reprompt(String conversation, String draft, List<String> names) {
        return conversation + "\n\nYOUR DRAFT ANSWER:\n" + draft
                + "\n\nThat draft names these that appear in none of the gathered data: " + list(names)
                + ".\nRe-write the answer without them. State only names and numbers the gathered data holds; "
                + "where the data does not show something, say it was not shown.";
    }

    /** A plain note, in the asker's terms, for names that stayed in the answer. */
    static String caveat(List<String> names) {
        return "\n\nNote: " + (names.size() == 1 ? "this name" : "these names")
                + " could not be matched to any data gathered for this answer, so treat "
                + (names.size() == 1 ? "it" : "them") + " as unverified: " + list(names) + ".";
    }

    /** The names as one line: at most {@link #MAX_LISTED}, then a count of the rest. */
    public static String list(List<String> names) {
        List<String> shown = names.size() > MAX_LISTED ? names.subList(0, MAX_LISTED) : names;
        String text = String.join(", ", shown);
        return names.size() > shown.size() ? text + " and " + (names.size() - shown.size()) + " more" : text;
    }
}
