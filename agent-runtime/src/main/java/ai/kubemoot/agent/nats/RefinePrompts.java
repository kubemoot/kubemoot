package ai.kubemoot.agent.nats;

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * The prompts of the synthesis rounds that read stored tool results into working notes. They are
 * runtime resources in ADL ({@code prompts/synthesis-notes-*.adl}), not Java text: the system prompt
 * and the notes format, the first round, the update rounds, and the section that hands the notes to
 * the crew's own synthesis prompt. A template holds {@code {{name}}} places that
 * {@link #render} fills in one pass, so text that was put in is never read as a place itself.
 */
final class RefinePrompts {

    private static final Pattern PLACE = Pattern.compile("\\{\\{(\\w+)}}");

    private static final String SYSTEM = load("synthesis-notes-system.adl");
    private static final String FIRST = load("synthesis-notes-first.adl");
    private static final String UPDATE = load("synthesis-notes-update.adl");
    private static final String FINAL = load("synthesis-notes-final.adl");

    private RefinePrompts() {}

    /** The system prompt of a notes round: the rules and the notes format. */
    static String system() {
        return SYSTEM;
    }

    /** The user prompt of a notes round; {@code notes} is null in the first round. */
    static String round(int round, String context, String notes, String batch) {
        var places = new HashMap<String, String>();
        places.put("round", Integer.toString(round));
        places.put("context", context);
        places.put("batch", batch);
        if (notes != null) {
            places.put("notes", notes);
        }
        return render(notes == null ? FIRST : UPDATE, places);
    }

    /** The section that hands the finished notes to the synthesis that writes the answer. */
    static String finalNotes(String notes) {
        return render(FINAL, Map.of("notes", notes));
    }

    static String render(String template, Map<String, String> places) {
        Matcher m = PLACE.matcher(template);
        var sb = new StringBuilder();
        while (m.find()) {
            String value = places.getOrDefault(m.group(1), m.group());
            m.appendReplacement(sb, Matcher.quoteReplacement(value));
        }
        m.appendTail(sb);
        return sb.toString();
    }

    static String load(String name) {
        try (InputStream in = RefinePrompts.class.getResourceAsStream("/prompts/" + name)) {
            if (in == null) {
                throw new IllegalStateException("missing prompt resource prompts/" + name);
            }
            return new String(in.readAllBytes(), StandardCharsets.UTF_8).strip();
        } catch (IOException e) {
            throw new IllegalStateException("cannot read prompt resource prompts/" + name, e);
        }
    }
}
